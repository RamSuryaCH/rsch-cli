package engineering

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/opencode-ai/opencode/internal/db"
)

func TestTaskStateMachineSupportsPauseAndRetry(t *testing.T) {
	if err := TransitionTask(StatusRunning, StatusPaused); err != nil {
		t.Fatal(err)
	}
	if err := TransitionTask(StatusPaused, StatusReady); err != nil {
		t.Fatal(err)
	}
	if err := TransitionTask(StatusFailed, StatusRetrying); err != nil {
		t.Fatal(err)
	}
	if err := TransitionTask(StatusRetrying, StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := TransitionTask(StatusCompleted, StatusRetrying); err == nil {
		t.Fatal("expected invalid completed retry transition")
	}
}

func TestDAGOrdersDependenciesDeterministically(t *testing.T) {
	ordered, err := DAG([]Task{{ID: "deploy", Dependencies: []string{"test"}}, {ID: "test", Dependencies: []string{"build"}}, {ID: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ordered[0].ID + "," + ordered[1].ID + "," + ordered[2].ID; got != "build,test,deploy" {
		t.Fatalf("unexpected order %s", got)
	}
}

func TestDAGRejectsUnsafeGraphs(t *testing.T) {
	for name, tasks := range map[string][]Task{
		"cycle":   {{ID: "a", Dependencies: []string{"b"}}, {ID: "b", Dependencies: []string{"a"}}},
		"missing": {{ID: "a", Dependencies: []string{"missing"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DAG(tasks); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestStoreRequiresEvidenceMetadata(t *testing.T) {
	store := NewStore()
	if err := store.PutEvidence(Evidence{ID: "e1"}); err == nil {
		t.Fatal("expected evidence validation error")
	}
}

func TestValidateCompletionRequiresEvidence(t *testing.T) {
	goal := Goal{ID: "g1"}
	task := Task{ID: "t1", Status: StatusPassed}
	if err := ValidateCompletion(goal, nil, []Task{task}, nil); err == nil {
		t.Fatal("expected missing evidence error")
	}
	if err := ValidateCompletion(goal, nil, []Task{task}, []Evidence{{ID: "e1", TaskID: "t1", Kind: "test", Passed: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLStorePersistsTaskExecutionMetadata(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := NewSQLStore(connection)
	if err := store.SaveGoal(context.Background(), Goal{ID: "goal-1", Title: "goal"}); err != nil {
		t.Fatal(err)
	}
	want := Task{ID: "task-1", GoalID: "goal-1", Title: "run", Command: []string{"go", "test", "./..."}, Timeout: 3 * time.Second, RetryLimit: 2}
	if err := store.ReplaceTasks(context.Background(), want.GoalID, []Task{want}); err != nil {
		t.Fatal(err)
	}
	items, err := store.LoadTasks(context.Background(), want.GoalID)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].GoalID != want.GoalID || items[0].Timeout != want.Timeout || items[0].RetryLimit != want.RetryLimit || len(items[0].Command) != 3 {
		t.Fatalf("task metadata=%+v", items[0])
	}
	all := store.Tasks()
	if len(all) != 1 || all[0].GoalID != want.GoalID || all[0].Timeout != want.Timeout {
		t.Fatalf("all tasks=%+v", all)
	}
}

func TestStoreLoadTasksWithoutGoalReturnsAllTasks(t *testing.T) {
	store := NewStore()
	if err := store.PutTasks([]Task{{ID: "b", GoalID: "second"}, {ID: "a", GoalID: "first"}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		goal string
		ids  []string
	}{
		{goal: "", ids: []string{"a", "b"}},
		{goal: "first", ids: []string{"a"}},
		{goal: "missing", ids: []string{}},
	} {
		items, err := store.LoadTasks(context.Background(), test.goal)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		if !reflect.DeepEqual(ids, test.ids) {
			t.Fatalf("goal %q: got %v, want %v", test.goal, ids, test.ids)
		}
	}
}

func TestStoreReplaceTasksValidatesBeforeChangingGoals(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	input := []Task{{ID: "original"}}
	if err := store.ReplaceTasks(ctx, "first", input); err != nil {
		t.Fatal(err)
	}
	if input[0].GoalID != "" {
		t.Fatal("replacement mutated caller's tasks")
	}
	if err := store.ReplaceTasks(ctx, "second", []Task{{ID: "other"}}); err != nil {
		t.Fatal(err)
	}
	want := store.Tasks()
	for _, test := range []struct {
		name  string
		goal  string
		tasks []Task
	}{
		{name: "missing goal", tasks: []Task{{ID: "new"}}},
		{name: "missing dependency", goal: "first", tasks: []Task{{ID: "new", Dependencies: []string{"missing"}}}},
		{name: "other goal's task", goal: "first", tasks: []Task{{ID: "new"}, {ID: "other"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.ReplaceTasks(ctx, test.goal, test.tasks); err == nil {
				t.Fatal("expected invalid replacement to fail")
			}
			if got := store.Tasks(); !reflect.DeepEqual(got, want) {
				t.Fatalf("failed replacement changed tasks: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestStoreReplaceTasksPublishesCompleteSnapshots(t *testing.T) {
	store := NewStore()
	ctx := context.Background()
	const taskCount = 32
	makeTasks := func(generation int) []Task {
		tasks := make([]Task, taskCount)
		for i := range tasks {
			tasks[i] = Task{ID: fmt.Sprintf("%03d-%03d", generation, i), Title: fmt.Sprint(generation)}
		}
		return tasks
	}
	if err := store.ReplaceTasks(ctx, "goal", makeTasks(0)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for generation := 1; generation <= 100; generation++ {
			if err := store.ReplaceTasks(ctx, "goal", makeTasks(generation)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		items, err := store.LoadTasks(ctx, "goal")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != taskCount {
			t.Fatalf("observed partial replacement: %d tasks", len(items))
		}
		for _, item := range items {
			if item.Title != items[0].Title {
				t.Fatal("observed tasks from different replacement generations")
			}
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestSQLStoreRejectsConcurrentStaleTaskTransition(t *testing.T) {
	directory := filepath.Join(t.TempDir(), ".rsch")
	connection, err := db.ConnectAt(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := NewSQLStore(connection)
	ctx := context.Background()
	if err := store.SaveGoal(ctx, Goal{ID: "goal", Title: "goal"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceTasks(ctx, "goal", []Task{{ID: "task", Status: StatusRunning}}); err != nil {
		t.Fatal(err)
	}

	// Pause both reads after SQLite has produced their snapshots so both
	// contenders validate the same running state before either can update it.
	read := make(chan struct{}, 2)
	release := make(chan struct{})
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(directory, "rsch.db"), RawQuery: "_pragma=busy_timeout(5000)"}
	concurrent, err := driver.Open(databaseURL.String(), func(conn *sqlite3.Conn) error {
		return conn.Trace(sqlite3.TRACE_PROFILE, func(_ sqlite3.TraceEvent, statement, _ any) error {
			query := statement.(*sqlite3.Stmt).SQL()
			if strings.HasPrefix(query, "SELECT ") && strings.Contains(query, "FROM workflow_tasks") {
				read <- struct{}{}
				<-release
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer concurrent.Close()
	concurrent.SetMaxOpenConns(2)
	results := make(chan error, 2)
	for _, status := range []Status{StatusCompleted, StatusCanceled} {
		go func(next Status) {
			_, err := NewSQLStore(concurrent).TransitionTask(ctx, "task", next)
			results <- err
		}(status)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-read:
		case err := <-results:
			close(release)
			t.Fatalf("transition returned before both reads: %v", err)
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("concurrent transitions did not read their initial state")
		}
	}
	close(release)
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "changed during transition") {
			t.Fatalf("unexpected transition error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful transitions from one state, want 1", successes)
	}
}
