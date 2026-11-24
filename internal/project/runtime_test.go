package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/sandbox"
)

type recordingRunner struct {
	called      bool
	argv        []string
	environment map[string]string
}

func (r *recordingRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	r.called = true
	r.argv = append([]string(nil), argv...)
	return []byte(argv[0]), nil
}

func (r *recordingRunner) RunWithEnvironment(_ context.Context, argv []string, environment map[string]string) ([]byte, error) {
	r.called = true
	r.argv = append([]string(nil), argv...)
	r.environment = environment
	return []byte(environment["RSCH_RESOURCE_ID"]), nil
}

func TestRuntimeValidatesCapabilitiesAndConfinesEntrypoint(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(entry, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	resource := Resource{Kind: "hook", Path: filepath.Join(dir, "hook.json"), Manifest: &Manifest{ID: "hook-1", Name: "Hook", Version: "1", Entrypoint: "hook.sh", Capabilities: []string{"filesystem.read"}}}
	runner := &recordingRunner{}
	r, err := NewRuntime(root, RuntimePolicy{AllowedCapabilities: map[string]bool{"filesystem.read": true}, ApprovedResources: map[string]bool{ApprovalKey("hook", "hook-1"): true}, Sandbox: runner, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), resource, Invocation{ResourceID: "hook-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "hook-1") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
	if !runner.called || runner.environment["RSCH_RESOURCE_KIND"] != "hook" {
		t.Fatalf("runtime did not pass its explicit resource environment: %+v", runner.environment)
	}
	resource.Manifest.Capabilities = []string{"network"}
	if _, err := r.Run(context.Background(), resource, Invocation{}); err == nil {
		t.Fatal("expected capability denial")
	}
	resource.Manifest.Capabilities = nil
	resource.Manifest.Entrypoint = "../outside"
	if _, err := r.Run(context.Background(), resource, Invocation{}); err == nil {
		t.Fatal("expected path confinement error")
	}
}

func TestRuntimeUsesConfiguredSandboxRunner(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "hook")
	if err := os.WriteFile(entry, []byte("ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	resource := Resource{Kind: "hook", Path: filepath.Join(dir, "hook.json"), Manifest: &Manifest{ID: "hook-2", Name: "Hook", Version: "1", Entrypoint: "hook"}}
	runtime, err := NewRuntime(root, RuntimePolicy{ApprovedResources: map[string]bool{ApprovalKey("hook", "hook-2"): true}, Sandbox: runner, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(context.Background(), resource, Invocation{})
	canonicalEntry, resolveErr := filepath.EvalSymlinks(entry)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err != nil || !runner.called || len(runner.argv) != 1 || runner.argv[0] != canonicalEntry || result.Output != "hook-2" {
		t.Fatalf("sandbox runner was not used: result=%+v err=%v called=%v", result, err, runner.called)
	}
}

func TestRuntimeEmptyCapabilitiesCannotBypassExplicitApproval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "hook")
	if err := os.WriteFile(entry, []byte("ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	resource := Resource{Kind: "hook", Path: filepath.Join(dir, "hook.json"), Manifest: &Manifest{ID: "empty-capabilities", Name: "Hook", Version: "1", Entrypoint: "hook"}}
	runner := &recordingRunner{}
	runtime, err := NewRuntime(root, RuntimePolicy{Sandbox: runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(context.Background(), resource, Invocation{}); !errors.Is(err, ErrResourceDenied) {
		t.Fatalf("error = %v, want ErrResourceDenied", err)
	}
	if runner.called {
		t.Fatal("unapproved resource entrypoint was executed")
	}
}

func TestRuntimeApprovedResourceStillRequiresSandbox(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "hook")
	if err := os.WriteFile(entry, []byte("ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	resource := Resource{Kind: "hook", Path: filepath.Join(dir, "hook.json"), Manifest: &Manifest{ID: "approved", Name: "Hook", Version: "1", Entrypoint: "hook"}}
	runtime, err := NewRuntime(root, RuntimePolicy{ApprovedResources: map[string]bool{ApprovalKey("hook", "approved"): true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(context.Background(), resource, Invocation{}); !errors.Is(err, ErrSandboxRequired) {
		t.Fatalf("error = %v, want ErrSandboxRequired", err)
	}
}

func TestRuntimeApprovalIsScopedByResourceKind(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "plugins")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "run")
	if err := os.WriteFile(entry, []byte("ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	resource := Resource{Kind: "plugin", Path: filepath.Join(dir, "plugin.json"), Manifest: &Manifest{ID: "shared-id", Name: "Plugin", Version: "1", Entrypoint: "run"}}
	runner := &recordingRunner{}
	runtime, err := NewRuntime(root, RuntimePolicy{ApprovedResources: map[string]bool{ApprovalKey("hook", "shared-id"): true}, Sandbox: runner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Run(context.Background(), resource, Invocation{}); !errors.Is(err, ErrResourceDenied) {
		t.Fatalf("error = %v, want kind-scoped ErrResourceDenied", err)
	}
	if runner.called {
		t.Fatal("plugin inherited a colliding hook approval")
	}
}

func TestRuntimeFailsClosedWhenSandboxIsRequiredButUnavailable(t *testing.T) {
	if sandbox.Detect().Isolated {
		t.Skip("host provides an OS sandbox")
	}
	if _, err := NewRuntime(t.TempDir(), RuntimePolicy{RequireSandbox: true}); err == nil {
		t.Fatal("expected required sandbox failure")
	}
}
