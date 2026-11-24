package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/project"
)

type hookFixtureRunner struct{}

func (hookFixtureRunner) Run(ctx context.Context, argv []string) ([]byte, error) {
	return hookFixtureRunner{}.RunWithEnvironment(ctx, argv, nil)
}

func (hookFixtureRunner) RunWithEnvironment(_ context.Context, argv []string, environment map[string]string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("missing fixture entrypoint")
	}
	switch filepath.Base(argv[0]) {
	case "a.sh":
		return []byte(environment["RSCH_EVENT_JSON"]), nil
	case "b.sh":
		return nil, nil
	case "closed.sh", "open.sh", "guard.sh", "observer.sh", "async-guard.sh":
		return nil, errors.New("fixture exit status 7")
	default:
		return nil, errors.New("unexpected fixture entrypoint")
	}
}

func writeHook(t *testing.T, root, name, id, body string, event string, policy string) {
	t.Helper()
	dir := filepath.Join(root, ".rsch", "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, name+".sh")
	if err := os.WriteFile(entry, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"` + id + `","name":"` + name + `","version":"1","entrypoint":"` + name + `.sh","events":["` + event + `"]`
	if policy != "" {
		manifest += `,"failurePolicy":"` + policy + `"`
	}
	manifest += `}`
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchMatchesOrdersAndPassesRedactedEvent(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "a", "a", "fixture", "tool.after", "")
	writeHook(t, root, "b", "b", "fixture", "tool.after", "")
	r, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("hook", "a"): true, project.ApprovalKey("hook", "b"): true}, Sandbox: hookFixtureRunner{}, MaxOutput: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(root, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := events.New(events.ToolAfter, map[string]any{"token": "secret-token-value"})
	results, err := m.Dispatch(context.Background(), e)
	if err != nil || len(results) != 2 {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if results[0].ResourceID != "a" || results[1].ResourceID != "b" {
		t.Fatalf("unexpected order: %+v", results)
	}
	if strings.Contains(results[0].Execution.Output, "secret-token-value") {
		t.Fatal("hook received an unredacted secret")
	}
}

func TestDispatchFailurePoliciesAndCancellation(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "closed", "closed", "fixture", "task.failed", "")
	r, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("hook", "closed"): true}, Sandbox: hookFixtureRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(root, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Dispatch(context.Background(), events.New(events.TaskFailed, nil)); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected fail-closed error, got %v", err)
	}
	root = t.TempDir()
	writeHook(t, root, "open", "open", "fixture", "task.failed", "fail-open")
	r, err = project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("hook", "open"): true}, Sandbox: hookFixtureRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err = New(root, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := m.Dispatch(context.Background(), events.New(events.TaskFailed, nil))
	if err != nil || len(results) != 1 || results[0].Err == "" {
		t.Fatalf("expected recorded fail-open result: %+v err=%v", results, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Dispatch(ctx, events.New(events.TaskFailed, nil)); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestToolInterceptorFailsClosedBeforeOperation(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "guard", "guard", "fixture", "tool.before", "")
	r, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("hook", "guard"): true}, Sandbox: hookFixtureRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(root, r, nil)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	event := events.New(events.ToolBefore, map[string]any{"tool": "write", "path": "protected.txt"})
	err = m.Interceptor()(context.Background(), event, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrFailedClosed) {
		t.Fatalf("interceptor error = %v, want ErrFailedClosed", err)
	}
	if called {
		t.Fatal("tool operation ran after a fail-closed hook failure")
	}
}

func TestToolInterceptorHonorsFailOpenAndOperationError(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "observer", "observer", "fixture", "before-tool", "fail-open")
	r, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("hook", "observer"): true}, Sandbox: hookFixtureRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(root, r, nil)
	if err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("tool failed")
	called := false
	err = m.InterceptTool(context.Background(), events.New(events.ToolBefore, nil), func(context.Context) error {
		called = true
		return wantErr
	})
	if !called {
		t.Fatal("fail-open hook prevented the tool operation")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("interceptor error = %v, want operation error", err)
	}
}

func TestToolInterceptorValidatesContract(t *testing.T) {
	m := &Manager{intercepted: make(map[string]struct{})}
	called := false
	if err := m.InterceptTool(context.Background(), events.New(events.ToolAfter, nil), func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, ErrPreToolEventRequired) {
		t.Fatalf("wrong event error = %v", err)
	}
	if called {
		t.Fatal("operation ran for a non-pre-tool event")
	}
	if err := m.InterceptTool(context.Background(), events.New(events.ToolBefore, nil), nil); !errors.Is(err, ErrToolOperationRequired) {
		t.Fatalf("nil operation error = %v", err)
	}
}

func TestStartRecordsAndReportsDispatchFailures(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "async-guard", "async-guard", "fixture", "task.failed", "")
	r, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("hook", "async-guard"): true}, Sandbox: hookFixtureRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	m, err := New(root, r, bus)
	if err != nil {
		t.Fatal(err)
	}
	reported := make(chan DispatchFailure, 1)
	m.SetErrorReporter(func(failure DispatchFailure) { reported <- failure })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Close()

	event := events.New(events.TaskFailed, map[string]any{"task": "test"})
	if !bus.Publish(event) {
		t.Fatal("event bus rejected task.failed event")
	}
	var failure DispatchFailure
	select {
	case failure = <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for asynchronous hook failure")
	}
	if failure.EventID != event.ID || failure.EventType != events.TaskFailed || failure.Error == "" || len(failure.Results) != 1 {
		t.Fatalf("failure = %+v", failure)
	}
	recorded := m.Failures()
	if len(recorded) != 1 || recorded[0].EventID != event.ID {
		t.Fatalf("recorded failures = %+v", recorded)
	}
	// Returned snapshots and reporter values must not be able to corrupt the
	// manager's retained evidence.
	recorded[0].Results[0].Err = "mutated"
	if m.Failures()[0].Results[0].Err == "mutated" {
		t.Fatal("Failures returned mutable internal state")
	}
}

func TestInterceptedEventIsConsumedOnlyOnce(t *testing.T) {
	m := &Manager{intercepted: make(map[string]struct{})}
	m.markIntercepted("event-1")
	m.markIntercepted("event-1")
	if !m.consumeIntercepted("event-1") {
		t.Fatal("intercepted event was not recognized")
	}
	if m.consumeIntercepted("event-1") {
		t.Fatal("intercepted event was consumed more than once")
	}
}

type cancellableHookRunner struct {
	started chan struct{}
	stopped chan struct{}
}

func (r *cancellableHookRunner) Run(ctx context.Context, _ []string) ([]byte, error) {
	r.started <- struct{}{}
	<-ctx.Done()
	r.stopped <- struct{}{}
	return nil, ctx.Err()
}

func TestCloseCancelsActiveHookAndAllowsRestart(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "a", "a", "fixture", "task.started", "")
	runner := &cancellableHookRunner{started: make(chan struct{}, 2), stopped: make(chan struct{}, 2)}
	r, err := project.NewRuntime(root, project.RuntimePolicy{
		ApprovedResources: map[string]bool{project.ApprovalKey("hook", "a"): true},
		Sandbox:           runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	m, err := New(root, r, bus)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Close()
	for i := 0; i < 2; i++ {
		m.Start(ctx)
		bus.Publish(events.New(events.TaskStarted, nil))
		select {
		case <-runner.started:
		case <-time.After(time.Second):
			t.Fatal("hook did not start")
		}
		// Queue another event while the first hook is running. Closing the
		// subscription must cancel both active work and queued dispatches.
		bus.Publish(events.New(events.TaskStarted, nil))
		m.Close()
		select {
		case <-runner.stopped:
		case <-time.After(time.Second):
			t.Fatal("Close did not cancel the active hook")
		}
	}
}

func TestParentCancellationReleasesSubscription(t *testing.T) {
	bus := events.NewBus()
	m := &Manager{bus: bus}
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	m.mu.RLock()
	subscription := m.subscription
	m.mu.RUnlock()
	cancel()
	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("subscription did not stop after parent cancellation")
	}
	m.Start(context.Background())
	defer m.Close()
	m.mu.RLock()
	restarted := m.subscription
	m.mu.RUnlock()
	if restarted == nil || restarted == subscription {
		t.Fatal("manager did not restart after parent cancellation")
	}
}

type cancellingHookRunner struct {
	cancel context.CancelFunc
	calls  int
}

func (r *cancellingHookRunner) Run(ctx context.Context, _ []string) ([]byte, error) {
	r.calls++
	r.cancel()
	return nil, ctx.Err()
}

func TestCancellationStopsFailOpenHooksAndToolOperation(t *testing.T) {
	root := t.TempDir()
	writeHook(t, root, "a", "a", "fixture", "tool.before", "fail-open")
	writeHook(t, root, "b", "b", "fixture", "tool.before", "fail-open")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &cancellingHookRunner{cancel: cancel}
	r, err := project.NewRuntime(root, project.RuntimePolicy{
		ApprovedResources: map[string]bool{project.ApprovalKey("hook", "a"): true, project.ApprovalKey("hook", "b"): true},
		Sandbox:           runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(root, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = m.InterceptTool(ctx, events.New(events.ToolBefore, nil), func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || runner.calls != 1 || called {
		t.Fatalf("cancellation was not honored: err=%v hook calls=%d tool called=%v", err, runner.calls, called)
	}
}
