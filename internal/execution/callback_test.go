package execution

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/events"
)

func TestExecuteCallbackUsesPolicyAndEvidence(t *testing.T) {
	executor, err := New(Config{Policy: DefaultPolicy{}, Sandbox: LocalSandbox{AllowedCommands: map[string]bool{"true": true}}, Autonomy: Autonomous})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.ExecuteCallback(context.Background(), Request{Tool: Spec{Name: "read", Risk: RiskLow}}, func(context.Context, Request) (string, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ok" || result.Evidence.Type != events.ToolAfter {
		t.Fatalf("unexpected callback result: %+v", result)
	}
}
