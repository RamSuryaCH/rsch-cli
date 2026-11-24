package permission

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
)

type permissionTestSandbox struct{}

func (permissionTestSandbox) Run(context.Context, execution.Request) (string, int, error) {
	return "ok", 0, nil
}

func TestPermissionDecisionsAreDurableEventsWhenConfigured(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	service := NewPermissionService(bus)
	result := make(chan bool, 1)
	go func() {
		result <- service.Request(CreatePermissionRequest{SessionID: "session-1", ToolName: "bash", Action: "execute", Path: "/workspace"})
	}()
	requested := <-stream
	if requested.Type != events.PermissionRequested || requested.SessionID != "session-1" {
		t.Fatalf("requested event=%+v", requested)
	}
	service.Grant(PermissionRequest{ID: requested.Data["permissionId"].(string), SessionID: "session-1", ToolName: "bash", Action: "execute", Path: "/workspace"})
	granted := <-stream
	if granted.Type != events.PermissionGranted {
		t.Fatalf("granted event=%+v", granted)
	}
	select {
	case approved := <-result:
		if !approved {
			t.Fatal("permission request was not approved")
		}
	case <-time.After(time.Second):
		t.Fatal("permission request did not finish")
	}
}

func TestPermissionRequestHonorsCancellation(t *testing.T) {
	service := NewPermissionService()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if service.RequestContext(ctx, CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace"}) {
		t.Fatal("canceled permission request was granted")
	}
}

func TestCanceledRequestCannotUseAutomaticOrPersistentGrant(t *testing.T) {
	for _, mode := range []string{"automatic", "persistent"} {
		t.Run(mode, func(t *testing.T) {
			bus := events.NewBus()
			stream, cancelStream := bus.Subscribe()
			defer cancelStream()
			service := NewPermissionService(bus)
			request := CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"}
			if mode == "automatic" {
				service.AutoApproveSession(request.SessionID)
			} else {
				result := make(chan bool, 1)
				go func() { result <- service.Request(request) }()
				select {
				case event := <-stream:
					service.GrantPersistant(PermissionRequest{ID: event.Data["permissionId"].(string)})
				case <-time.After(time.Second):
					t.Fatal("permission prompt was not published")
				}
				if !<-result {
					t.Fatal("initial persistent grant was rejected")
				}
				<-stream // Consume the initial grant event.
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if service.RequestContext(ctx, request) {
				t.Fatal("canceled request reused permission")
			}
			select {
			case event := <-stream:
				t.Fatalf("canceled request emitted an authorization event: %v", event.Type)
			default:
			}
		})
	}
}

func TestPermissionDuplicateDecisionsDoNotBlockOrChangeFirstDecision(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	service := NewPermissionService(bus)
	result := make(chan bool, 1)
	go func() {
		result <- service.Request(CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"})
	}()
	request := <-stream
	id := request.Data["permissionId"].(string)
	first := PermissionRequest{ID: id, SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace"}
	done := make(chan struct{}, 1)
	go func() {
		service.Grant(first)
		service.Deny(first)
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("duplicate permission decisions blocked")
	}
	select {
	case approved := <-result:
		if !approved {
			t.Fatal("first permission decision was not retained")
		}
	case <-time.After(time.Second):
		t.Fatal("permission request did not finish")
	}
}

func TestForgedPersistentGrantCannotCreatePermission(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	service := NewPermissionService(bus)

	service.GrantPersistant(PermissionRequest{ID: "not-pending", SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace"})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	result := make(chan bool, 1)
	go func() {
		result <- service.RequestContext(ctx, CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"})
	}()
	select {
	case event := <-stream:
		if event.Type != events.PermissionRequested {
			t.Fatalf("unexpected event after forged grant: %v", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("forged persistent grant bypassed the permission prompt")
	}
	service.Deny(PermissionRequest{ID: "not-pending"})
	stop()
	if <-result {
		t.Fatal("permission was granted after forged persistent grant")
	}
}

func TestPersistentGrantUsesOriginalPendingRequest(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	service := NewPermissionService(bus)
	result := make(chan bool, 1)
	go func() {
		result <- service.Request(CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"})
	}()
	requested := <-stream
	permissionID := requested.Data["permissionId"].(string)
	service.GrantPersistant(PermissionRequest{ID: permissionID, SessionID: "attacker", ToolName: "admin", Action: "execute", Path: "/"})
	if !<-result {
		t.Fatal("original pending request was not granted")
	}

	ctx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if service.RequestContext(ctx, CreatePermissionRequest{SessionID: "attacker", ToolName: "admin", Action: "execute", Path: "/"}) {
		t.Fatal("mutated decision fields created a persistent permission")
	}
}

func TestEmptyPermissionIdentityFailsClosedEvenForAutoApprovedSession(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	service := NewPermissionService(bus)
	service.AutoApproveSession("")
	if service.RequestContext(context.Background(), CreatePermissionRequest{ToolName: "write", Action: "write"}) {
		t.Fatal("empty permission identity was auto-approved")
	}
	select {
	case event := <-stream:
		t.Fatalf("invalid request emitted an authorization event: %v", event.Type)
	default:
	}
}

func TestGovernedPermissionAllowsHighRiskRequestToReachUserPrompt(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	base := NewPermissionService(bus)
	executor, err := execution.New(execution.Config{Policy: execution.DefaultPolicy{}, Sandbox: permissionTestSandbox{}, Autonomy: execution.Autonomous, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	service := NewGovernedService(base, executor)
	result := make(chan bool, 1)
	go func() {
		result <- service.RequestContext(context.Background(), CreatePermissionRequest{SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace/file"})
	}()
	var permissionID string
	deadline := time.After(time.Second)
	for permissionID == "" {
		select {
		case event := <-stream:
			if event.Type == events.PermissionRequested {
				if event.Data["path"] != "/workspace" || event.Data["scopeKind"] != ScopeFile {
					t.Fatalf("governed file scope = path %#v kind %#v", event.Data["path"], event.Data["scopeKind"])
				}
				permissionID = event.Data["permissionId"].(string)
			}
		case <-deadline:
			t.Fatal("governed request never reached permission prompt")
		}
	}
	base.Grant(PermissionRequest{ID: permissionID, SessionID: "session-1", ToolName: "write", Action: "write", Path: "/workspace"})
	select {
	case approved := <-result:
		if !approved {
			t.Fatal("approved high-risk request was denied")
		}
	case <-time.After(time.Second):
		t.Fatal("governed permission request did not finish")
	}
}

func TestPermissionScopeKindsPreserveExactAuthority(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		kind     ScopeKind
		wantPath string
	}{
		{name: "file uses containing directory", path: "/workspace/project/main.go", kind: ScopeFile, wantPath: "/workspace/project"},
		{name: "directory remains exact", path: "/workspace/project", kind: ScopeDirectory, wantPath: "/workspace/project"},
		{name: "workspace root remains exact", path: "/workspace", kind: ScopeDirectory, wantPath: "/workspace"},
		{name: "URL is not treated as a filesystem path", path: "https://example.com/api/v1?q=a/b", kind: ScopeURL, wantPath: "https://example.com/api/v1?q=a/b"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bus := events.NewBus()
			stream, cancelStream := bus.Subscribe()
			defer cancelStream()
			service := NewPermissionService(bus)
			ctx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			result := make(chan bool, 1)
			go func() {
				result <- service.RequestContext(ctx, CreatePermissionRequest{
					SessionID: fmt.Sprintf("session-%d", index),
					ToolName:  "scope-test",
					Action:    "access",
					Path:      test.path,
					ScopeKind: test.kind,
				})
			}()
			select {
			case event := <-stream:
				if event.Type != events.PermissionRequested {
					t.Fatalf("unexpected event: %v", event.Type)
				}
				if event.Data["path"] != test.wantPath || event.Data["scopeKind"] != test.kind {
					t.Fatalf("scope event = path %#v kind %#v", event.Data["path"], event.Data["scopeKind"])
				}
				service.Deny(PermissionRequest{ID: event.Data["permissionId"].(string)})
			case <-time.After(time.Second):
				t.Fatal("permission request was not published")
			}
			if <-result {
				t.Fatal("denied scope request was granted")
			}
		})
	}
}

func TestPermissionRejectsUnknownOrEmptyOpaqueScope(t *testing.T) {
	service := NewPermissionService()
	base := CreatePermissionRequest{SessionID: "session-1", ToolName: "scope-test", Action: "access", Path: "/workspace"}
	base.ScopeKind = ScopeKind("unknown")
	if service.RequestContext(context.Background(), base) {
		t.Fatal("unknown permission scope kind was accepted")
	}
	base.ScopeKind = ScopeURL
	base.Path = ""
	if service.RequestContext(context.Background(), base) {
		t.Fatal("empty URL permission scope was accepted")
	}
}
