package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
)

func TestPromptWorkflowPersistsLifecycleAndEvidence(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	bus := events.NewBus()
	bus.AddSink(events.NewSQLLog(connection))
	app := &App{Workflow: engineering.NewSQLStore(connection), EventBus: bus}
	defer app.EventBus.Close()
	workflow, err := app.beginPromptWorkflow(context.Background(), "implement an auditable feature", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	app.finishPromptWorkflow(workflow, true, nil)
	goals := app.Workflow.Goals()
	if len(goals) != 1 || goals[0].Status != engineering.StatusCompleted {
		t.Fatalf("goals=%+v", goals)
	}
	tasks, err := app.Workflow.LoadTasks(context.Background(), workflow.Goal.ID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != engineering.StatusVerified {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	evidence, err := app.Workflow.LoadEvidence(context.Background(), workflow.Task.ID)
	if err != nil || len(evidence) != 1 || !evidence[0].Passed {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	events, err := events.NewSQLLog(connection).Replay(context.Background(), 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 {
		t.Fatalf("expected lifecycle events, got %d", len(events))
	}
}

func TestFailedPromptWorkflowPublishesTaskTerminalEvent(t *testing.T) {
	for _, test := range []struct {
		name       string
		runError   error
		wantType   events.Type
		wantStatus engineering.Status
	}{
		{name: "failure", runError: errors.New("provider failed"), wantType: events.TaskFailed, wantStatus: engineering.StatusFailed},
		{name: "cancellation", runError: context.Canceled, wantType: events.TaskCanceled, wantStatus: engineering.StatusCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			bus := events.NewBus()
			defer bus.Close()
			bus.AddSink(events.NewSQLLog(connection))
			application := &App{Workflow: engineering.NewSQLStore(connection), EventBus: bus}
			workflow, err := application.beginPromptWorkflow(context.Background(), "do work", "session-1")
			if err != nil {
				t.Fatal(err)
			}
			if err := application.finishPromptWorkflow(workflow, false, test.runError); err != nil {
				t.Fatal(err)
			}
			replayed, err := events.NewSQLLog(connection).Replay(context.Background(), 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range replayed {
				if event.Type == test.wantType && event.TaskID == workflow.Task.ID && event.GoalID == workflow.Goal.ID {
					tasks, err := application.Workflow.LoadTasks(context.Background(), workflow.Goal.ID)
					if err != nil || len(tasks) != 1 || tasks[0].Status != test.wantStatus {
						t.Fatalf("tasks = %+v, error = %v", tasks, err)
					}
					return
				}
			}
			t.Fatalf("missing %s event for task %s", test.wantType, workflow.Task.ID)
		})
	}
}

func TestWorkflowEventsRedactFailureMessagesForLiveSubscribers(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	application := &App{EventBus: bus}
	application.publishWorkflowEvent(events.TaskFailed, "goal-1", "provider rejected Authorization: Bearer fixture-token", map[string]any{"taskId": "task-1"})
	select {
	case event := <-stream:
		message, ok := event.Data["message"].(string)
		if !ok || strings.Contains(message, "fixture-token") || !strings.Contains(message, "[REDACTED]") {
			t.Fatalf("live failure event contains an unredacted message: %q", message)
		}
	default:
		t.Fatal("failure event was not delivered")
	}
}

func TestPromptWorkflowCannotVerifyWithoutDurableEvidence(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	application := &App{Workflow: engineering.NewSQLStore(connection), EventBus: events.NewBus()}
	defer application.EventBus.Close()
	workflow, err := application.beginPromptWorkflow(context.Background(), "do work", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = connection.Exec(`CREATE TRIGGER reject_passed_evidence BEFORE INSERT ON evidence WHEN NEW.passed = 1 BEGIN SELECT RAISE(FAIL, 'evidence storage unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.finishPromptWorkflow(workflow, true, nil); err == nil || !strings.Contains(err.Error(), "evidence storage unavailable") {
		t.Fatalf("error = %v, want evidence storage failure", err)
	}
	tasks, err := application.Workflow.LoadTasks(context.Background(), workflow.Goal.ID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != engineering.StatusFailed {
		t.Fatalf("tasks = %+v, error = %v; failed evidence must not verify a task", tasks, err)
	}
	goals := application.Workflow.Goals()
	if len(goals) != 1 || goals[0].Status != engineering.StatusFailed {
		t.Fatalf("goals = %+v, want failed", goals)
	}
}
