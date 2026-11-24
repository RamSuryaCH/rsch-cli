package tools

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
)

type contractTestTool struct{}

func (contractTestTool) Info() ToolInfo {
	return ToolInfo{Name: "contract-test", Parameters: map[string]any{"value": map[string]any{"type": "string"}}}
}

func (contractTestTool) Run(context.Context, ToolCall) (ToolResponse, error) {
	return NewTextResponse("ok"), nil
}

type recordingContractTool struct {
	runs int
}

type responseContractTool struct {
	response ToolResponse
	err      error
}

func (responseContractTool) Info() ToolInfo {
	return ToolInfo{Name: "response-test"}
}

func (t responseContractTool) Run(context.Context, ToolCall) (ToolResponse, error) {
	return t.response, t.err
}

func (t *recordingContractTool) Info() ToolInfo {
	return ToolInfo{Name: "restricted-tool"}
}

func (t *recordingContractTool) Run(context.Context, ToolCall) (ToolResponse, error) {
	t.runs++
	return NewTextResponse("ran"), nil
}

func newGovernedTestExecutor(t *testing.T, policy execution.Policy, autonomy execution.Autonomy) *execution.Executor {
	t.Helper()
	executor, err := execution.New(execution.Config{
		Policy:   policy,
		Sandbox:  execution.LocalSandbox{AllowedCommands: map[string]bool{"true": true}},
		Autonomy: autonomy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func TestGovernedToolPublishesCompleteExecutionContract(t *testing.T) {
	bus := events.NewBus()
	executor, err := execution.New(execution.Config{
		Policy:   execution.DefaultPolicy{DelegatedApproval: true},
		Sandbox:  execution.LocalSandbox{AllowedCommands: map[string]bool{"true": true}},
		Autonomy: execution.Autonomous,
		Bus:      bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	info := Govern(contractTestTool{}, executor, execution.RiskHigh).Info()
	if info.Risk != string(execution.RiskHigh) || len(info.Permissions) == 0 || info.Timeout <= 0 || info.ExecutionMode == "" || len(info.Capabilities) == 0 || info.OutputSchema == nil {
		t.Fatalf("incomplete tool contract: %+v", info)
	}
	stream, cancel := bus.Subscribe()
	defer cancel()
	ctx := context.WithValue(context.Background(), TaskIDContextKey, "task-telemetry")
	if _, err := Govern(contractTestTool{}, executor, execution.RiskLow).Run(ctx, ToolCall{ID: "call-1", Name: "contract-test"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-stream:
		if event.Type != events.ToolBefore || event.TaskID != "task-telemetry" || event.Data["taskId"] != "task-telemetry" {
			t.Fatalf("task context was not propagated: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("execution event not published")
	}
}

func TestGovernedToolEnforcesConfiguredDenyPathAndCommandRules(t *testing.T) {
	tests := []struct {
		name   string
		policy execution.DefaultPolicy
		input  string
	}{
		{
			name:   "deny tool",
			policy: execution.DefaultPolicy{DeniedTools: map[string]bool{"restricted-tool": true}},
			input:  `{}`,
		},
		{
			name:   "deny path",
			policy: execution.DefaultPolicy{AllowedPaths: []string{"/workspace/allowed"}},
			input:  `{"path":"/workspace/blocked/file.txt"}`,
		},
		{
			name:   "deny command",
			policy: execution.DefaultPolicy{AllowedCommands: map[string]bool{"go": true}},
			input:  `{"command":"rm -rf /tmp/example"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inner := &recordingContractTool{}
			executor := newGovernedTestExecutor(t, test.policy, execution.Autonomous)
			_, err := Govern(inner, executor, execution.RiskLow).Run(context.Background(), ToolCall{ID: "restricted-call", Input: test.input})
			if !errors.Is(err, execution.ErrDenied) {
				t.Fatalf("expected policy denial, got %v", err)
			}
			if inner.runs != 0 {
				t.Fatal("governed callback ran after policy denial")
			}
		})
	}
}

func TestGovernedToolEnforcesConfiguredAutonomy(t *testing.T) {
	inner := &recordingContractTool{}
	executor := newGovernedTestExecutor(t, execution.DefaultPolicy{DelegatedApproval: true}, execution.Manual)
	_, err := Govern(inner, executor, execution.RiskHigh).Run(context.Background(), ToolCall{ID: "manual-call", Input: `{}`})
	if !errors.Is(err, execution.ErrApprovalRequired) {
		t.Fatalf("expected approval requirement, got %v", err)
	}
	if inner.runs != 0 {
		t.Fatal("governed callback ran without configured approval")
	}
}

func TestConfiguredCommandAllowlistPreservesExactExecutablePaths(t *testing.T) {
	policy := execution.DefaultPolicy{AllowedCommands: configuredCommandAllowlist([]string{" /approved/bin/tool ", "git", " "})}
	for _, test := range []struct {
		command string
		want    execution.Decision
	}{
		{command: "/approved/bin/tool", want: execution.Allow},
		{command: "git", want: execution.Allow},
		{command: "tool", want: execution.Deny},
		{command: "/workspace/tool", want: execution.Deny},
		{command: "/workspace/git", want: execution.Deny},
	} {
		request := execution.Request{Tool: execution.Spec{Risk: execution.RiskLow}, Command: []string{test.command}}
		if got := policy.Evaluate(request); got != test.want {
			t.Errorf("configured policy for %q = %q, want %q", test.command, got, test.want)
		}
	}
}

func TestInputCommandPreservesExecutableIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]any
		want   []string
	}{
		{name: "quoted path", fields: map[string]any{"command": `"/approved/bin/tool with spaces" --version`}, want: []string{"/approved/bin/tool with spaces"}},
		{name: "escaped path", fields: map[string]any{"command": `/approved/bin/tool\ with\ spaces --version`}, want: []string{"/approved/bin/tool with spaces"}},
		{name: "quoted literal backslash", fields: map[string]any{"command": `"g\o" version`}, want: []string{`g\o`}},
		{name: "literal argv quotes", fields: map[string]any{"argv": []any{"'tool'", "argument"}}, want: []string{"'tool'"}},
		{name: "literal argv spaces", fields: map[string]any{"argv": []any{" tool ", "argument"}}, want: []string{" tool "}},
		{name: "non-shell whitespace", fields: map[string]any{"command": "tool\u00a0argument"}, want: []string{"tool\u00a0argument"}},
		{name: "trailing non-shell whitespace", fields: map[string]any{"command": "tool\u00a0"}, want: []string{"tool\u00a0"}},
		{name: "empty argv executable", fields: map[string]any{"argv": []any{""}}, want: []string{""}},
		{name: "invalid shell quoting", fields: map[string]any{"command": `"tool`}, want: []string{""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := inputCommand(test.fields); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("governance executable = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestReadOnlyExecutorAcceptsInjectedConfiguredKernel(t *testing.T) {
	inner := &recordingContractTool{}
	executor, err := ReadOnlyExecutor(nil, execution.Config{
		Policy:   execution.DefaultPolicy{DeniedTools: map[string]bool{"restricted-tool": true}},
		Sandbox:  execution.LocalSandbox{AllowedCommands: map[string]bool{"true": true}},
		Autonomy: execution.Autonomous,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Govern(inner, executor, execution.RiskLow).Run(context.Background(), ToolCall{ID: "injected-call", Input: `{}`})
	if !errors.Is(err, execution.ErrDenied) {
		t.Fatalf("injected policy was not enforced: %v", err)
	}
	if inner.runs != 0 {
		t.Fatal("callback ran after injected policy denial")
	}
}

func TestGovernedToolFailsClosedWithoutExecutor(t *testing.T) {
	inner := &recordingContractTool{}
	_, err := Govern(inner, nil, execution.RiskLow).Run(context.Background(), ToolCall{ID: "unwired-call", Input: `{}`})
	if err == nil {
		t.Fatal("unwired governed tool unexpectedly ran")
	}
	if inner.runs != 0 {
		t.Fatal("unwired governed callback ran")
	}
}

func TestGovernedToolRecordsErrorResponsesAsFailedEvidence(t *testing.T) {
	transportErr := errors.New("transport failed")
	for _, test := range []struct {
		name     string
		response ToolResponse
		err      error
		terminal events.Type
	}{
		{name: "success", response: NewTextResponse("ok"), terminal: events.ToolAfter},
		{name: "model error", response: WithResponseMetadata(NewTextErrorResponse("Exit code 1"), map[string]any{"exit": 1}), terminal: events.ToolFailed},
		{name: "empty model error", response: NewTextErrorResponse(""), terminal: events.ToolFailed},
		{name: "transport error", response: NewTextErrorResponse("connection lost"), err: transportErr, terminal: events.ToolFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			bus := events.NewBus()
			executor, err := execution.New(execution.Config{
				Policy: execution.DefaultPolicy{}, Sandbox: execution.LocalSandbox{},
				Autonomy: execution.Autonomous, Bus: bus,
			})
			if err != nil {
				t.Fatal(err)
			}
			stream, cancel := bus.Subscribe()
			defer cancel()
			inner := responseContractTool{response: test.response, err: test.err}
			response, err := Govern(inner, executor, execution.RiskLow).Run(context.Background(), ToolCall{ID: "response-call", Input: `{}`})
			if err != test.err || !reflect.DeepEqual(response, test.response) {
				t.Fatalf("tool response contract changed: response=%+v err=%v", response, err)
			}
			for {
				select {
				case event := <-stream:
					if event.Type != events.ToolAfter && event.Type != events.ToolFailed {
						continue
					}
					if event.Type != test.terminal {
						t.Fatalf("terminal evidence = %s, want %s", event.Type, test.terminal)
					}
					return
				case <-time.After(time.Second):
					t.Fatal("terminal tool evidence was not published")
				}
			}
		})
	}
}

func TestInputPathsIncludesPatchMoveDestinationOnlyFromHeaders(t *testing.T) {
	paths := inputPaths(map[string]any{"patch_text": `*** Begin Patch
*** Update File: source.txt
*** Move to: destination.txt
@@
-File: content-is-not-a-path
+File: still-not-a-path
*** Add File: added.txt
+content
*** Delete File: removed.txt
*** End Patch`})
	want := []string{"source.txt", "destination.txt", "added.txt", "removed.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("governance patch paths = %#v, want %#v", paths, want)
	}
}
