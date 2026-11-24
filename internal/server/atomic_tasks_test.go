package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
)

type taskSnapshotBarrier struct {
	reads atomic.Int32
	ready chan struct{}
}

type snapshotBarrierSQLStore struct {
	*engineering.SQLStore
	barrier *taskSnapshotBarrier
}

func (s *snapshotBarrierSQLStore) LoadTasks(ctx context.Context, goalID string) ([]engineering.Task, error) {
	tasks, err := s.SQLStore.LoadTasks(ctx, goalID)
	if read := s.barrier.reads.Add(1); read <= 2 {
		if read == 2 {
			close(s.barrier.ready)
		}
		select {
		case <-s.barrier.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tasks, err
}

func TestTaskMutationsAcrossServerInstancesPreserveChanges(t *testing.T) {
	for _, operation := range []string{"create", "edit"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".rsch")
			firstDB, err := db.ConnectAt(path)
			if err != nil {
				t.Fatal(err)
			}
			defer firstDB.Close()
			secondDB, err := db.ConnectAt(path)
			if err != nil {
				t.Fatal(err)
			}
			defer secondDB.Close()
			firstStore, secondStore := engineering.NewSQLStore(firstDB), engineering.NewSQLStore(secondDB)
			if err := firstStore.SaveGoal(context.Background(), engineering.Goal{ID: "goal-1", Title: "goal"}); err != nil {
				t.Fatal(err)
			}
			if operation == "edit" {
				if err := firstStore.ReplaceTasks(context.Background(), "goal-1", []engineering.Task{{ID: "first", Title: "old first"}, {ID: "second", Title: "old second"}}); err != nil {
					t.Fatal(err)
				}
			}
			barrier := &taskSnapshotBarrier{ready: make(chan struct{})}
			services := []*Server{
				NewWithLog(nil, &snapshotBarrierSQLStore{firstStore, barrier}, events.NewSQLLog(firstDB)),
				NewWithLog(nil, &snapshotBarrierSQLStore{secondStore, barrier}, events.NewSQLLog(secondDB)),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var done sync.WaitGroup
			responses := []*httptest.ResponseRecorder{httptest.NewRecorder(), httptest.NewRecorder()}
			for i, id := range []string{"first", "second"} {
				done.Add(1)
				go func(i int, id string) {
					defer done.Done()
					method, target, status := http.MethodPost, "/v1/tasks", http.StatusCreated
					if operation == "edit" {
						method, target, status = http.MethodPut, "/v1/tasks/"+id, http.StatusOK
					}
					request := newServerRequest(method, target, strings.NewReader(`{"id":"`+id+`","goalId":"goal-1","title":"`+id+`"}`)).WithContext(ctx)
					services[i].ServeHTTP(responses[i], request)
					if responses[i].Code != status {
						t.Errorf("%s status=%d body=%s", id, responses[i].Code, responses[i].Body.String())
					}
				}(i, id)
			}
			done.Wait()
			tasks, err := firstStore.LoadTasks(context.Background(), "goal-1")
			if err != nil || len(tasks) != 2 {
				t.Fatalf("cross-server mutation lost tasks: %+v err=%v", tasks, err)
			}
			for _, task := range tasks {
				if task.Title != task.ID {
					t.Fatalf("cross-server mutation lost edit: %+v", task)
				}
			}
		})
	}
}

func TestTaskMutationRollsBackWhenEventCannotPersist(t *testing.T) {
	for _, test := range []struct{ name, method, path, body string }{
		{"create", http.MethodPost, "/v1/tasks", `{"id":"new","goalId":"goal-1","title":"new"}`},
		{"edit", http.MethodPut, "/v1/tasks/original", `{"title":"edited"}`},
		{"replace", http.MethodPut, "/v1/tasks?goalId=goal-1", `[{"id":"replacement","title":"replacement"}]`},
		{"nested replace", http.MethodPost, "/v1/goals/goal-1/tasks", `[{"id":"replacement","title":"replacement"}]`},
		{"transition", http.MethodPost, "/v1/tasks/original/transition", `{"status":"ready"}`},
		{"nested transition", http.MethodPost, "/v1/goals/goal-1/tasks/original/transition", `{"status":"ready"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			store := engineering.NewSQLStore(connection)
			if err := store.SaveGoal(context.Background(), engineering.Goal{ID: "goal-1", Title: "goal"}); err != nil {
				t.Fatal(err)
			}
			if err := store.ReplaceTasks(context.Background(), "goal-1", []engineering.Task{{ID: "original", Title: "original", Status: engineering.StatusPending}}); err != nil {
				t.Fatal(err)
			}
			if _, err := connection.Exec(`CREATE TRIGGER reject_task_event BEFORE INSERT ON event_log BEGIN SELECT RAISE(ABORT, 'event storage unavailable'); END`); err != nil {
				t.Fatal(err)
			}
			bus := events.NewBus()
			stream, unsubscribe := bus.Subscribe()
			defer unsubscribe()
			service := NewWithLog(bus, store, events.NewSQLLog(connection))
			response := httptest.NewRecorder()
			service.ServeHTTP(response, newServerRequest(test.method, test.path, strings.NewReader(test.body)))
			if response.Code < 400 {
				t.Errorf("event failure reported successful mutation: status=%d body=%s", response.Code, response.Body.String())
			}
			tasks, err := store.LoadTasks(context.Background(), "goal-1")
			if err != nil || len(tasks) != 1 || tasks[0].ID != "original" || tasks[0].Title != "original" || tasks[0].Status != engineering.StatusPending {
				t.Errorf("event failure did not roll back mutation: %+v err=%v", tasks, err)
			}
			select {
			case event := <-stream:
				t.Errorf("rolled-back mutation emitted event: %+v", event)
			default:
			}
		})
	}
}

func TestAtomicTaskMutationPublishesOneCommittedEvent(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := engineering.NewSQLStore(connection)
	if err := store.SaveGoal(context.Background(), engineering.Goal{ID: "goal-1", Title: "goal"}); err != nil {
		t.Fatal(err)
	}
	log := events.NewSQLLog(connection)
	bus := events.NewBus()
	bus.AddSink(log)
	var diagnostics bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, nil)))
	defer slog.SetDefault(previousLogger)
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	response := httptest.NewRecorder()
	NewWithLog(bus, store, log).ServeHTTP(response, newServerRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"id":"first","goalId":"goal-1","title":"first"}`)))
	var task engineering.Task
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &task) != nil || task.ID != "first" {
		t.Fatalf("task creation failed: status=%d body=%s", response.Code, response.Body.String())
	}
	replayed, err := log.Replay(context.Background(), 0, 10)
	if err != nil || len(replayed) != 1 || replayed[0].TaskID != task.ID {
		t.Fatalf("committed event missing or duplicated: %+v err=%v", replayed, err)
	}
	select {
	case live := <-stream:
		if live.Sequence != replayed[0].Sequence || live.ID != replayed[0].ID {
			t.Fatalf("live event does not match committed event: %+v", live)
		}
	default:
		t.Fatal("committed event was not delivered")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("committed event was redundantly persisted or delivery failed: %s", diagnostics.String())
	}
}
