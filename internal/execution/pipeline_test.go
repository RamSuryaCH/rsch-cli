package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/notification"
)

type fixedPolicy Decision

func (p fixedPolicy) Evaluate(Request) Decision { return Decision(p) }

type fakeSandbox struct{}

func (fakeSandbox) Run(context.Context, Request) (string, int, error) { return "ok", 0, nil }

type recordingNotifier struct{ kinds []string }

func (n *recordingNotifier) Notify(_ context.Context, event notification.Event) error {
	n.kinds = append(n.kinds, event.Kind)
	return nil
}

func TestExecutorDeniesPolicyBeforeSandbox(t *testing.T) {
	bus := events.NewBus()
	_, cancel := bus.Subscribe()
	defer cancel()
	e, err := New(Config{Policy: DefaultPolicy{DeniedTools: map[string]bool{"danger": true}}, Sandbox: fakeSandbox{}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), Request{Tool: Spec{Name: "danger", Risk: RiskLow}}); err != ErrDenied {
		t.Fatalf("got %v", err)
	}
}

func TestExecutorHonorsPromptToolPolicy(t *testing.T) {
	e, err := New(Config{Policy: DefaultPolicy{PromptTools: map[string]bool{"write": true}}, Sandbox: fakeSandbox{}, Autonomy: Autonomous})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), Request{Tool: Spec{Name: "write", Risk: RiskLow}}); err != ErrApprovalRequired {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultPolicyRestrictionsOverridePromptDecision(t *testing.T) {
	policy := DefaultPolicy{
		PromptTools:     map[string]bool{"write": true},
		AllowedPaths:    []string{"/workspace/allowed"},
		AllowedCommands: map[string]bool{"go": true},
	}
	if decision := policy.Evaluate(Request{Tool: Spec{Name: "write", Risk: RiskLow}, Path: "/workspace/blocked"}); decision != Deny {
		t.Fatalf("path restriction was bypassed by prompt decision: %s", decision)
	}
	if decision := policy.Evaluate(Request{Tool: Spec{Name: "write", Risk: RiskLow}, Command: []string{"rm"}}); decision != Deny {
		t.Fatalf("command restriction was bypassed by prompt decision: %s", decision)
	}
}

func TestExecutorRequiresApprovalForHighRisk(t *testing.T) {
	notifier := &recordingNotifier{}
	e, err := New(Config{Policy: DefaultPolicy{}, Sandbox: fakeSandbox{}, Autonomy: Manual, Notifier: notifier})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), Request{Tool: Spec{Name: "write", Risk: RiskHigh}}); err != ErrApprovalRequired {
		t.Fatalf("got %v", err)
	}
	if len(notifier.kinds) != 1 || notifier.kinds[0] != "approval.required" {
		t.Fatalf("unexpected notifications: %+v", notifier.kinds)
	}
}

func TestDelegatedApprovalBoundaryRunsHighRiskCallback(t *testing.T) {
	approved := false
	e, err := New(Config{Policy: DefaultPolicy{}, Sandbox: fakeSandbox{}, Autonomy: Autonomous})
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.ExecuteCallbackWithApproval(context.Background(), Request{Tool: Spec{Name: "write", Risk: RiskHigh}}, func(context.Context, Request) (string, error) {
		return "written", nil
	}, func(context.Context, Request) (bool, error) {
		approved = true
		return true, nil
	})
	if err != nil || !approved || result.Output != "written" {
		t.Fatalf("delegated approval failed: result=%+v approved=%t err=%v", result, approved, err)
	}
}

func TestExecutorRejectsUnknownPolicyDecisions(t *testing.T) {
	for _, decision := range []Decision{"", "ALLOW", "invalid"} {
		t.Run(string(decision), func(t *testing.T) {
			executor, err := New(Config{Policy: fixedPolicy(decision), Sandbox: fakeSandbox{}, Autonomy: Autonomous})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = executor.ExecuteCallback(context.Background(), Request{Tool: Spec{Name: "read", Risk: RiskLow}}, func(context.Context, Request) (string, error) {
				called = true
				return "", nil
			})
			if !errors.Is(err, ErrDenied) || called {
				t.Fatalf("unknown decision executed callback: err=%v called=%t", err, called)
			}
		})
	}
}

func TestExecutorDoesNotStartCanceledCallbacks(t *testing.T) {
	for _, test := range []struct {
		name           string
		duringApproval bool
	}{
		{name: "before execution"},
		{name: "during approval", duringApproval: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := fixedPolicy(Allow)
			if test.duringApproval {
				policy = fixedPolicy(Prompt)
			} else {
				cancel()
			}
			executor, err := New(Config{Policy: policy, Sandbox: fakeSandbox{}, Autonomy: Autonomous,
				Approval: func(context.Context, Request) (bool, error) { cancel(); return true, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = executor.ExecuteCallback(ctx, Request{Tool: Spec{Name: "write", Risk: RiskHigh}}, func(context.Context, Request) (string, error) {
				called = true
				return "", nil
			})
			if !errors.Is(err, context.Canceled) || called {
				t.Fatalf("canceled callback started: err=%v called=%t", err, called)
			}
		})
	}
}

func TestExecutorDoesNotRecordSuccessAfterCallbackDeadline(t *testing.T) {
	executor, err := New(Config{Policy: fixedPolicy(Allow), Sandbox: fakeSandbox{}, Autonomy: Autonomous, MaxTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.ExecuteCallback(context.Background(), Request{Tool: Spec{Name: "read", Risk: RiskLow}}, func(ctx context.Context, _ Request) (string, error) {
		<-ctx.Done()
		return "partial", nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || result.Evidence.Type == events.ToolAfter {
		t.Fatalf("expired callback recorded success: result=%+v err=%v", result, err)
	}
}

func TestExecutorRecordsApprovalFailure(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	executor, err := New(Config{Policy: fixedPolicy(Prompt), Sandbox: fakeSandbox{}, Bus: bus,
		Approval: func(context.Context, Request) (bool, error) { return false, context.Canceled },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), Request{Tool: Spec{Name: "write", Risk: RiskHigh}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("approval error was lost: %v", err)
	}
	for _, want := range []events.Type{events.ToolBefore, events.SandboxRequested, events.SandboxDenied, events.ToolFailed} {
		select {
		case event := <-stream:
			if event.Type != want {
				t.Fatalf("event=%s want=%s", event.Type, want)
			}
		default:
			t.Fatalf("missing lifecycle event: %s", want)
		}
	}
}

func TestCommandAllowlistRequiresExactExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX script executable")
	}
	path := filepath.Join(t.TempDir(), "go")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf unapproved"), 0700); err != nil {
		t.Fatal(err)
	}
	request := Request{Tool: Spec{Name: "command", Risk: RiskLow}, Command: []string{path}}
	allowed := map[string]bool{"go": true}
	if got := (DefaultPolicy{AllowedCommands: allowed}).Evaluate(request); got != Deny {
		t.Errorf("unapproved executable with approved basename was allowed: %s", got)
	}
	if output, _, err := (LocalSandbox{AllowedCommands: allowed}).Run(context.Background(), request); err == nil || output != "" {
		t.Errorf("unapproved executable ran: output=%q err=%v", output, err)
	}
	allowed[path] = true
	if got := (DefaultPolicy{AllowedCommands: allowed}).Evaluate(request); got != Allow {
		t.Errorf("explicitly approved executable was rejected: %s", got)
	}
	if output, code, err := (LocalSandbox{AllowedCommands: allowed}).Run(context.Background(), request); err != nil || code != 0 || output != "unapproved" {
		t.Errorf("explicitly approved executable failed: output=%q code=%d err=%v", output, code, err)
	}
}
