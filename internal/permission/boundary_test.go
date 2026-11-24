package permission

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
)

type permissionPolicyFunc func(execution.Request) execution.Decision

func (f permissionPolicyFunc) Evaluate(request execution.Request) execution.Decision {
	return f(request)
}

type permissionSinkFunc func(context.Context, events.Event) error

func (f permissionSinkFunc) Append(ctx context.Context, event events.Event) error {
	return f(ctx, event)
}

func TestGovernedPermissionFailsClosedWithoutExecutor(t *testing.T) {
	base := NewPermissionService()
	base.AutoApproveSession("session-1")
	service := NewGovernedService(base, nil)
	if service.RequestContext(context.Background(), CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"}) {
		t.Fatal("missing governance executor bypassed policy")
	}
}

func TestGovernedPermissionUsesCanonicalIdentityBeforePolicy(t *testing.T) {
	for _, test := range []struct {
		name    string
		request CreatePermissionRequest
		policy  execution.Policy
	}{
		{
			name:    "denied tool",
			request: CreatePermissionRequest{SessionID: "session-1", ToolName: " write ", Action: "write", Path: "/workspace/file"},
			policy:  execution.DefaultPolicy{DeniedTools: map[string]bool{"write": true}},
		},
		{
			name:    "high risk action",
			request: CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: " write ", Path: "/workspace/file"},
			policy: permissionPolicyFunc(func(request execution.Request) execution.Decision {
				if request.Tool.Risk == execution.RiskHigh {
					return execution.Deny
				}
				return execution.Allow
			}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := NewPermissionService()
			base.AutoApproveSession("session-1")
			executor, err := execution.New(execution.Config{Policy: test.policy, Sandbox: permissionTestSandbox{}, Autonomy: execution.Autonomous})
			if err != nil {
				t.Fatal(err)
			}
			if NewGovernedService(base, executor).RequestContext(context.Background(), test.request) {
				t.Fatal("permission identity normalization bypassed policy")
			}
		})
	}
}

func TestPermissionGrantIsRecordedBeforeCallerContinues(t *testing.T) {
	bus := events.NewBus()
	stream, stopStream := bus.Subscribe()
	defer stopStream()
	entered, release := make(chan struct{}), make(chan struct{})
	bus.AddSink(permissionSinkFunc(func(_ context.Context, event events.Event) error {
		if event.Type == events.PermissionGranted {
			close(entered)
			<-release
		}
		return nil
	}))
	service := NewPermissionService(bus)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan bool, 1)
	go func() {
		result <- service.RequestContext(ctx, CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"})
	}()
	requested := <-stream
	grantDone := make(chan struct{})
	go func() {
		service.Grant(PermissionRequest{ID: requested.Data["permissionId"].(string)})
		close(grantDone)
	}()
	<-entered
	var early bool
	select {
	case <-result:
		early = true
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	<-grantDone
	if early {
		t.Fatal("caller continued before the grant event reached its sink")
	}
	if !<-result {
		t.Fatal("grant was lost after event publication")
	}
}

func TestCanceledGrantDoesNotEmitConflictingTerminalDecisions(t *testing.T) {
	bus := events.NewBus()
	stream, stopStream := bus.Subscribe()
	defer stopStream()
	entered, release := make(chan struct{}), make(chan struct{})
	bus.AddSink(permissionSinkFunc(func(_ context.Context, event events.Event) error {
		if event.Type == events.PermissionGranted {
			close(entered)
			<-release
		}
		return nil
	}))
	service := NewPermissionService(bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan bool, 1)
	go func() {
		result <- service.RequestContext(ctx, CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"})
	}()
	requested := <-stream
	grantDone := make(chan struct{})
	go func() {
		service.Grant(PermissionRequest{ID: requested.Data["permissionId"].(string)})
		close(grantDone)
	}()
	<-entered
	cancel()
	close(release)
	<-grantDone
	<-result
	terminalEvents := 0
	for {
		select {
		case event := <-stream:
			if event.Type == events.PermissionGranted || event.Type == events.PermissionDenied {
				terminalEvents++
			}
		default:
			if terminalEvents != 1 {
				t.Fatalf("one request emitted %d terminal decisions", terminalEvents)
			}
			return
		}
	}
}

func TestPersistentPermissionReuseIsAudited(t *testing.T) {
	bus := events.NewBus()
	stream, stopStream := bus.Subscribe()
	defer stopStream()
	service := NewPermissionService(bus)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"}
	result := make(chan bool, 1)
	go func() { result <- service.RequestContext(ctx, request) }()
	requested := <-stream
	service.GrantPersistant(PermissionRequest{ID: requested.Data["permissionId"].(string)})
	if !<-result {
		t.Fatal("initial grant failed")
	}
	<-stream
	if !service.RequestContext(ctx, request) {
		t.Fatal("persistent permission was not reused")
	}
	select {
	case event := <-stream:
		if event.Type != events.PermissionAutoGranted || event.Data["persistent"] != true || event.SessionID != request.SessionID {
			t.Fatalf("unexpected reused-grant event: %+v", event)
		}
	default:
		t.Fatal("persistent permission reuse has no audit event")
	}
}
