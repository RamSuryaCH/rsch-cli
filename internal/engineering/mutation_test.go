package engineering

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/events"
)

func mutationSQLStore(t *testing.T) (*SQLStore, *sql.DB, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), ".rsch")
	connection, err := db.ConnectAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	store := NewSQLStore(connection)
	if err := store.SaveGoal(context.Background(), Goal{ID: "goal", Title: "Goal"}); err != nil {
		t.Fatal(err)
	}
	return store, connection, directory
}

func TestSQLMutateTasksSerializesSeparateConnections(t *testing.T) {
	first, connection, directory := mutationSQLStore(t)
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(directory, "rsch.db"), RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}
	other, err := driver.Open(databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// These are independent connection pools and store instances, as used by
	// separate HTTP servers sharing the same persisted workflow.
	connection.SetMaxOpenConns(1)
	other.SetMaxOpenConns(1)
	stores := []*SQLStore{first, NewSQLStore(other)}
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(index int) {
			<-start
			id := fmt.Sprintf("task-%02d", index)
			_, event, err := stores[index%len(stores)].MutateTasks(context.Background(), "goal", func(tasks []Task) ([]Task, events.Event, error) {
				tasks = append(tasks, Task{ID: id, Title: id})
				event := events.New(events.TaskCreated, map[string]any{"count": len(tasks)})
				event.TaskID = id
				return tasks, event, nil
			})
			if err == nil && event.Sequence <= 0 {
				err = errors.New("committed task event has no durable sequence")
			}
			results <- err
		}(i)
	}
	close(start)
	for i := 0; i < writers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := first.LoadTasks(context.Background(), "goal")
	if err != nil || len(tasks) != writers {
		t.Fatalf("concurrent task creates lost updates: tasks=%+v err=%v", tasks, err)
	}
	log, err := events.NewSQLLog(connection).Replay(context.Background(), 0, writers+1)
	if err != nil || len(log) != writers {
		t.Fatalf("expected one durable event for each task: events=%+v err=%v", log, err)
	}
	for i, event := range log {
		if event.GoalID != "goal" || event.Data["count"] != float64(i+1) {
			t.Fatalf("mutation did not see preceding commit at event %d: %+v", i, event)
		}
	}
}

func TestSQLMutateTasksRollsBackWhenEventPersistenceFails(t *testing.T) {
	store, connection, _ := mutationSQLStore(t)
	ctx := context.Background()
	if err := store.ReplaceTasks(ctx, "goal", []Task{{ID: "original", Title: "Original"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddEvidence(ctx, Evidence{ID: "proof", TaskID: "original", Kind: "test", Passed: true}); err != nil {
		t.Fatal(err)
	}
	event := events.New(events.TasksUpdated, nil)
	if err := events.NewSQLLog(connection).Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	// A duplicate durable ID causes the event insert to fail after the task
	// update and deletion have executed; all of those writes must roll back.
	result, returnedEvent, err := store.MutateTasks(ctx, "goal", func([]Task) ([]Task, events.Event, error) {
		return []Task{{ID: "replacement"}}, event, nil
	})
	if err == nil || result != nil || returnedEvent.Sequence != 0 {
		t.Fatalf("expected failed mutation without a committed event: result=%+v event=%+v err=%v", result, returnedEvent, err)
	}
	tasks, err := store.LoadTasks(ctx, "goal")
	if err != nil || len(tasks) != 1 || tasks[0].ID != "original" || tasks[0].Title != "Original" {
		t.Fatalf("event failure changed tasks: tasks=%+v err=%v", tasks, err)
	}
	evidence, err := store.LoadEvidence(ctx, "original")
	if err != nil || len(evidence) != 1 {
		t.Fatalf("event failure removed evidence: evidence=%+v err=%v", evidence, err)
	}
	log, err := events.NewSQLLog(connection).Replay(ctx, 0, 10)
	if err != nil || len(log) != 1 {
		t.Fatalf("failed mutation changed event log: events=%+v err=%v", log, err)
	}
}

func TestSQLTaskReplacementPreservesRetainedEvidence(t *testing.T) {
	store, _, _ := mutationSQLStore(t)
	ctx := context.Background()
	if err := store.ReplaceTasks(ctx, "goal", []Task{{ID: "keep"}, {ID: "remove"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"keep", "remove"} {
		if err := store.AddEvidence(ctx, Evidence{ID: "proof-" + id, TaskID: id, Kind: "test", Passed: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceTasks(ctx, "goal", []Task{{ID: "keep", Title: "Updated"}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id    string
		count int
	}{{id: "keep", count: 1}, {id: "remove", count: 0}} {
		evidence, err := store.LoadEvidence(ctx, test.id)
		if err != nil || len(evidence) != test.count {
			t.Fatalf("evidence for %s: got %+v err=%v, want %d items", test.id, evidence, err, test.count)
		}
	}
}

func TestMutateTasksRejectsInvalidChangesWithoutMutatingSnapshots(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			var store interface {
				TransactionalTaskStore
				ReplaceTasks(context.Context, string, []Task) error
				LoadTasks(context.Context, string) ([]Task, error)
			}
			if backend == "sql" {
				store, _, _ = mutationSQLStore(t)
			} else {
				store = NewStore()
			}
			ctx := context.Background()
			original := []Task{{ID: "original", Command: []string{"go", "test"}, Skills: []string{"review"}}}
			if err := store.ReplaceTasks(ctx, "goal", original); err != nil {
				t.Fatal(err)
			}
			want, err := store.LoadTasks(ctx, "goal")
			if err != nil {
				t.Fatal(err)
			}
			for _, failure := range []string{"callback", "dag", "event", "goal", "canceled"} {
				t.Run(failure, func(t *testing.T) {
					mutationCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					_, event, err := store.MutateTasks(mutationCtx, "goal", func(tasks []Task) ([]Task, events.Event, error) {
						tasks[0].Command[0] = "changed"
						tasks[0].Skills[0] = "changed"
						event := events.New(events.TasksUpdated, nil)
						switch failure {
						case "callback":
							return nil, events.Event{}, errors.New("rejected change")
						case "dag":
							tasks[0].Dependencies = []string{"missing"}
						case "event":
							event.Data = map[string]any{"invalid": func() {}}
						case "goal":
							event.GoalID = "another-goal"
						case "canceled":
							cancel()
						}
						return tasks, event, nil
					})
					if err == nil || event.Sequence != 0 {
						t.Fatalf("invalid mutation should fail: event=%+v err=%v", event, err)
					}
					got, err := store.LoadTasks(ctx, "goal")
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("failed callback modified stored tasks: got=%+v want=%+v err=%v", got, want, err)
					}
				})
			}
		})
	}
}

func TestStoreMutateTasksReturnsIsolatedNormalizedTasks(t *testing.T) {
	store := NewStore()
	task := Task{ID: "task", Title: "password: \"" + "test-secret-material" + "\"", Command: []string{"go", "test"}}
	tasks, event, err := store.MutateTasks(context.Background(), "goal", func([]Task) ([]Task, events.Event, error) {
		return []Task{task}, events.New(events.TaskCreated, nil), nil
	})
	if err != nil || len(tasks) != 1 || tasks[0].Status != StatusPending || tasks[0].GoalID != "goal" || event.Sequence != 0 {
		t.Fatalf("unexpected mutation: tasks=%+v event=%+v err=%v", tasks, event, err)
	}
	if strings.Contains(tasks[0].Title, "test-secret-material") {
		t.Fatal("mutation returned unredacted task metadata")
	}
	task.Command[0] = "caller changed input"
	tasks[0].Command[1] = "caller changed output"
	got, err := store.LoadTasks(context.Background(), "goal")
	if err != nil || !reflect.DeepEqual(got[0].Command, []string{"go", "test"}) {
		t.Fatalf("mutation exposed mutable stored command: tasks=%+v err=%v", got, err)
	}
}

func TestStoreMutateTasksSerializesConcurrentCreates(t *testing.T) {
	store := NewStore()
	const writers = 12
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, _, err := store.MutateTasks(context.Background(), "goal", func(tasks []Task) ([]Task, events.Event, error) {
				return append(tasks, Task{ID: fmt.Sprint(index)}), events.New(events.TaskCreated, nil), nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	tasks, err := store.LoadTasks(context.Background(), "goal")
	if err != nil || len(tasks) != writers {
		t.Fatalf("concurrent mutation lost tasks: tasks=%+v err=%v", tasks, err)
	}
}
