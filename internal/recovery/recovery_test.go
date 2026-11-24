package recovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
)

type fakeSandbox struct{}

func (fakeSandbox) Run(context.Context, execution.Request) (string, int, error) {
	return "replayed", 0, nil
}

func TestAnalyzeIdentifiesIncompleteAndFailedActions(t *testing.T) {
	before := events.New(events.ToolBefore, map[string]any{"requestId": "a", "tool": "write", "risk": execution.RiskHigh})
	before.TaskID = "task-1"
	failed := events.New(events.ToolFailed, map[string]any{"requestId": "b", "tool": "test", "error": "exit 1"})
	failed.TaskID = "task-1"
	after := events.New(events.ToolAfter, map[string]any{"requestId": "c", "tool": "read"})
	after.TaskID = "task-1"
	plan := Analyze([]events.Event{before, failed, after}, "task-1")
	if len(plan.Incomplete) != 2 || plan.Failures != 1 || !plan.SafeInspect {
		t.Fatalf("unexpected recovery plan: %+v", plan)
	}
	if plan.Actions[0].RequestID != "a" || plan.Actions[0].Status != "incomplete" || plan.Actions[0].Risk != execution.RiskHigh {
		t.Fatalf("actions not deterministic: %+v", plan.Actions)
	}
}

func TestReexecuteDelegatesThroughExecutor(t *testing.T) {
	executor, err := execution.New(execution.Config{Policy: execution.DefaultPolicy{}, Sandbox: fakeSandbox{}, Autonomy: execution.Autonomous})
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{Incomplete: []string{"request-1"}, Actions: []Action{{RequestID: "request-1", Tool: "read", Status: "incomplete"}}, SafeInspect: true}
	results, err := Reexecute(context.Background(), executor, plan, func(action Action) (execution.Request, bool) {
		return execution.Request{Tool: execution.Spec{Name: action.Tool, Risk: execution.RiskLow}}, true
	})
	if err != nil || len(results) != 1 || results[0].Output != "replayed" {
		t.Fatalf("unexpected replay result: %+v err=%v", results, err)
	}
}

func TestAnalyzeOnlyTracksToolLifecycleEventsWithStringIDs(t *testing.T) {
	items := []events.Event{
		events.New(events.ModelRequest, map[string]any{"requestId": "model-request"}),
		events.New(events.SandboxRequested, map[string]any{"requestId": "sandbox-request"}),
		events.New(events.ToolBefore, map[string]any{"requestId": nil}),
		events.New(events.ToolBefore, map[string]any{"requestId": 123}),
		events.New(events.ToolBefore, map[string]any{"requestId": "tool-request", "taskId": "task-1"}),
	}
	for i := 0; i < len(items)-1; i++ {
		items[i].TaskID = "task-1"
	}
	other := events.New(events.ToolBefore, map[string]any{"requestId": "other-task"})
	other.TaskID = "task-2"
	items = append(items, other)
	plan := Analyze(items, "task-1")
	if plan.EventCount != 5 || len(plan.Actions) != 1 || len(plan.Incomplete) != 1 || plan.Incomplete[0] != "tool-request" {
		t.Fatalf("non-tool or malformed events became replayable actions: %+v", plan)
	}
}

func TestAnalyzeUsesEventOrderAndClearsEarlierRetryFailures(t *testing.T) {
	for _, sequenced := range []bool{true, false} {
		name := "legacy timestamps"
		if sequenced {
			name = "durable sequence with clock skew"
		}
		t.Run(name, func(t *testing.T) {
			start := time.Now().UTC()
			items := []events.Event{
				events.New(events.ToolBefore, map[string]any{"requestId": "request-1", "tool": "test"}),
				events.New(events.ToolFailed, map[string]any{"requestId": "request-1", "error": "old failure"}),
				events.New(events.ToolBefore, map[string]any{"requestId": "request-1", "tool": "test"}),
				events.New(events.ToolAfter, map[string]any{"requestId": "request-1"}),
			}
			for i := range items {
				items[i].At = start.Add(time.Duration(i) * time.Second)
				if sequenced {
					items[i].Sequence = int64(i + 1)
					items[i].At = start.Add(-time.Duration(i) * time.Second)
				}
			}
			shuffled := []events.Event{items[3], items[1], items[2], items[0]}
			original := append([]events.Event(nil), shuffled...)
			plan := Analyze(shuffled, "")
			if len(plan.Actions) != 1 || plan.Actions[0].Status != "completed" || plan.Actions[0].Error != "" || len(plan.Incomplete) != 0 || plan.Failures != 0 {
				t.Fatalf("completed retry was misclassified: %+v", plan)
			}
			if !reflect.DeepEqual(original, shuffled) {
				t.Fatal("analysis reordered the caller's event slice")
			}
			plan = Analyze([]events.Event{items[2], items[0], items[1]}, "")
			if plan.Actions[0].Status != "incomplete" || plan.Actions[0].Error != "" || plan.Failures != 0 {
				t.Fatalf("new retry retained stale failure: %+v", plan)
			}
		})
	}
}

func TestReexecuteRejectsInvalidPlansBeforeResolvingAnyRequests(t *testing.T) {
	executor, err := execution.New(execution.Config{Policy: execution.DefaultPolicy{}, Sandbox: fakeSandbox{}, Autonomy: execution.Autonomous})
	if err != nil {
		t.Fatal(err)
	}
	valid := Action{RequestID: "valid", Tool: "read", Status: "incomplete", TaskID: "task-1"}
	for name, plan := range map[string]Plan{
		"missing action":    {Incomplete: []string{"valid", "missing"}, Actions: []Action{valid}},
		"completed action":  {Incomplete: []string{"valid", "complete"}, Actions: []Action{valid, {RequestID: "complete", Status: "completed"}}},
		"unknown status":    {Incomplete: []string{"valid", "unknown"}, Actions: []Action{valid, {RequestID: "unknown"}}},
		"duplicate request": {Incomplete: []string{"valid", "valid"}, Actions: []Action{valid}},
		"ambiguous action":  {Incomplete: []string{"valid"}, Actions: []Action{valid, valid}},
		"empty identity":    {Incomplete: []string{"valid", ""}, Actions: []Action{valid, {}}},
		"different task":    {TaskID: "task-2", Incomplete: []string{"valid"}, Actions: []Action{valid}},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			results, err := Reexecute(context.Background(), executor, plan, func(action Action) (execution.Request, bool) {
				calls++
				return execution.Request{Tool: execution.Spec{Name: "read", Risk: execution.RiskLow}}, true
			})
			if err == nil || calls != 0 || len(results) != 0 {
				t.Fatalf("invalid plan started replay: results=%+v resolver calls=%d err=%v", results, calls, err)
			}
		})
	}
}

func TestReexecuteCanceledContextDoesNotResolveRequests(t *testing.T) {
	executor, err := execution.New(execution.Config{Policy: execution.DefaultPolicy{}, Sandbox: fakeSandbox{}, Autonomy: execution.Autonomous})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := Plan{Incomplete: []string{"request-1"}, Actions: []Action{{RequestID: "request-1", Tool: "read", Status: "failed"}}}
	_, err = Reexecute(ctx, executor, plan, func(action Action) (execution.Request, bool) {
		t.Fatal("resolver ran after cancellation")
		return execution.Request{}, false
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled recovery, got %v", err)
	}
}
