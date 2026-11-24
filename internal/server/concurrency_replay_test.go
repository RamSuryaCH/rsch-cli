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

type blockedTaskStore struct {
	GoalStore
	replacements atomic.Int32
	firstWrite   chan struct{}
	releaseWrite chan struct{}
	secondRead   chan struct{}
	readOnce     sync.Once
}

func (s *blockedTaskStore) LoadTasks(ctx context.Context, goalID string) ([]engineering.Task, error) {
	if s.replacements.Load() == 1 {
		s.readOnce.Do(func() { close(s.secondRead) })
	}
	return s.GoalStore.LoadTasks(ctx, goalID)
}

func (s *blockedTaskStore) ReplaceTasks(ctx context.Context, goalID string, tasks []engineering.Task) error {
	if s.replacements.Add(1) == 1 {
		close(s.firstWrite)
		<-s.releaseWrite
	}
	return s.GoalStore.ReplaceTasks(ctx, goalID, tasks)
}

func TestConcurrentTaskMutationsPreserveCommittedChanges(t *testing.T) {
	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       string
		statusCode int
		wantTasks  int
		wantTitle  string
		wantStatus engineering.Status
		cancel     bool
	}{
		{"create", http.MethodPost, "/v1/tasks", `{"id":"second","goalId":"goal-1","title":"second"}`, http.StatusCreated, 3, "original", engineering.StatusPending, false},
		{"edit", http.MethodPut, "/v1/tasks/original", `{"title":"edited"}`, http.StatusOK, 2, "edited", engineering.StatusPending, false},
		{"transition", http.MethodPost, "/v1/tasks/original/transition", `{"status":"ready"}`, http.StatusOK, 2, "original", engineering.StatusReady, false},
		{"canceled edit", http.MethodPut, "/v1/tasks/original", `{"title":"edited"}`, http.StatusRequestTimeout, 2, "original", engineering.StatusPending, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := engineering.NewStore()
			if err := base.ReplaceTasks(context.Background(), "goal-1", []engineering.Task{{ID: "original", Title: "original", Status: engineering.StatusPending}}); err != nil {
				t.Fatal(err)
			}
			store := &blockedTaskStore{GoalStore: base, firstWrite: make(chan struct{}), releaseWrite: make(chan struct{}), secondRead: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(store.releaseWrite) }) }
			defer release()
			service := New(nil, store)
			first := httptest.NewRecorder()
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				service.ServeHTTP(first, newServerRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"id":"first","goalId":"goal-1","title":"first"}`)))
			}()
			select {
			case <-store.firstWrite:
			case <-time.After(3 * time.Second):
				t.Fatal("first task mutation did not reach persistence")
			}
			second := httptest.NewRecorder()
			secondDone := make(chan struct{})
			secondRequest := newServerRequest(test.method, test.path, strings.NewReader(test.body))
			if test.cancel {
				ctx, cancel := context.WithCancel(secondRequest.Context())
				cancel()
				secondRequest = secondRequest.WithContext(ctx)
			}
			go func() {
				defer close(secondDone)
				service.ServeHTTP(second, secondRequest)
			}()
			// A read for a second mutation must wait until the first write has
			// committed; otherwise its replacement is based on a stale snapshot.
			select {
			case <-store.secondRead:
				t.Error("second mutation read tasks before the first committed")
			case <-time.After(50 * time.Millisecond):
			}
			release()
			for _, done := range []chan struct{}{firstDone, secondDone} {
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("task mutation did not complete")
				}
			}
			if first.Code != http.StatusCreated || second.Code != test.statusCode {
				t.Fatalf("mutation statuses=%d, %d; bodies=%s, %s", first.Code, second.Code, first.Body.String(), second.Body.String())
			}
			tasks := base.Tasks()
			if len(tasks) != test.wantTasks {
				t.Fatalf("concurrent mutation lost tasks: %+v", tasks)
			}
			for _, task := range tasks {
				if task.ID == "original" && (task.Title != test.wantTitle || task.Status != test.wantStatus) {
					t.Fatalf("concurrent mutation lost task update: %+v", task)
				}
			}
		})
	}
}

func TestFilteredReplayAppliesLimitToMatchesAcrossPages(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	log := events.NewSQLLog(connection)
	for i := 0; i < 1003; i++ {
		event := events.New(events.TaskCompleted, nil)
		if i >= 1001 {
			event.Type = events.AgentMessage
			event.GoalID, event.TaskID, event.SessionID = "target-goal", "target-task", "target-session"
		}
		if err := log.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	service := NewWithLog(nil, nil, log)
	for _, test := range []struct {
		query string
		first int64
		count int
	}{
		{"after=0&limit=2", 1002, 2},
		{"after=1002&limit=1", 1003, 1},
	} {
		response := httptest.NewRecorder()
		service.ServeHTTP(response, newServerRequest(http.MethodGet, "/v1/replay?"+test.query+"&goalId=target-goal&taskId=target-task&sessionId=target-session&type=agent.message", nil))
		var replayed []events.Event
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &replayed) != nil {
			t.Fatalf("replay status=%d body=%s", response.Code, response.Body.String())
		}
		if len(replayed) != test.count || replayed[0].Sequence != test.first {
			t.Fatalf("filtered replay omitted matches: %+v", replayed)
		}
	}
	// Workspace messages and replay detail use this same helper.
	replayed, err := service.replayEvents(context.Background(), eventFilter{TaskID: "target-task"}, 1)
	if err != nil || len(replayed) != 1 || replayed[0].Sequence != 1002 {
		t.Fatalf("workspace replay omitted matches: %+v, err=%v", replayed, err)
	}
}

func TestCommittedMutationLogsEventPersistenceFailureWithoutExposingPayload(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, nil)))
	defer slog.SetDefault(previousLogger)
	store := engineering.NewStore()
	service := NewWithLog(nil, store, events.NewSQLLog(connection))
	response := httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodPost, "/v1/goals", strings.NewReader(`{"id":"goal-1","title":"private event payload"}`)))
	if response.Code != http.StatusCreated || len(store.Goals()) != 1 {
		t.Fatalf("committed mutation incorrectly encouraged a retry: status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(diagnostics.String(), "event persistence failed") || !strings.Contains(diagnostics.String(), "eventId=") {
		t.Fatalf("persistence failure was not diagnosed: %s", diagnostics.String())
	}
	if strings.Contains(diagnostics.String(), "private event payload") {
		t.Fatalf("event payload leaked into diagnostics: %s", diagnostics.String())
	}
}

type contextCheckingEventSink struct {
	gotErr error
}

func (s *contextCheckingEventSink) Append(ctx context.Context, _ events.Event) error {
	s.gotErr = ctx.Err()
	return s.gotErr
}

func TestPublishPreservesCancellationAndReportsDeliveryFailure(t *testing.T) {
	var diagnostics bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&diagnostics, nil)))
	defer slog.SetDefault(previousLogger)
	sink := &contextCheckingEventSink{}
	bus := events.NewBus()
	bus.AddSink(sink)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	New(bus, nil).publish(ctx, events.New(events.TaskCompleted, nil))
	if sink.gotErr != context.Canceled || !strings.Contains(diagnostics.String(), "event delivery failed") {
		t.Fatalf("delivery failure lost cancellation or diagnostics: err=%v logs=%s", sink.gotErr, diagnostics.String())
	}
}
