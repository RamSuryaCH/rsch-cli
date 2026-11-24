package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeRejectsResourcesOutsideWorkspace(t *testing.T) {
	for _, scenario := range []string{"external resource", "symlinked directory", "symlinked entrypoint", "entrypoint in sibling directory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			dir := filepath.Join(root, ".rsch", "hooks")
			if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
				t.Fatal(err)
			}
			if scenario == "symlinked directory" {
				if err := os.Symlink(outside, dir); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			} else if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if scenario == "external resource" {
				dir = outside
			}
			entry := filepath.Join(dir, "run")
			if scenario == "symlinked entrypoint" || scenario == "entrypoint in sibling directory" {
				target := filepath.Join(outside, "run")
				if scenario == "entrypoint in sibling directory" {
					target = filepath.Join(root, "run")
				}
				if err := os.WriteFile(target, []byte("fixture"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, entry); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			} else if err := os.WriteFile(entry, []byte("fixture"), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{}
			runtime, err := NewRuntime(root, RuntimePolicy{Sandbox: runner, ApprovedResources: map[string]bool{ApprovalKey("hook", "approved"): true}})
			if err != nil {
				t.Fatal(err)
			}
			resource := Resource{Kind: "hook", Path: filepath.Join(dir, "hook.json"), Manifest: &Manifest{ID: "approved", Entrypoint: "run"}}
			if _, err := runtime.Run(context.Background(), resource, Invocation{}); !errors.Is(err, ErrEntrypointInvalid) {
				t.Fatalf("error = %v, want ErrEntrypointInvalid", err)
			}
			if runner.called {
				t.Fatal("runtime executed an entrypoint outside its resource boundary")
			}
		})
	}
}

func TestRuntimeAllowsSymlinksWithinResourceDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "hooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "run")
	if err := os.WriteFile(entry, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entry, filepath.Join(dir, "entrypoint")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	rootLink := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runner := &recordingRunner{}
	runtime, err := NewRuntime(rootLink, RuntimePolicy{Sandbox: runner, ApprovedResources: map[string]bool{ApprovalKey("hook", "approved"): true}})
	if err != nil {
		t.Fatal(err)
	}
	resource := Resource{Kind: "hook", Path: filepath.Join(rootLink, ".rsch", "hooks", "hook.json"), Manifest: &Manifest{ID: "approved", Entrypoint: "entrypoint"}}
	if _, err := runtime.Run(context.Background(), resource, Invocation{}); err != nil {
		t.Fatal(err)
	}
	canonicalEntry, err := filepath.EvalSymlinks(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.called || len(runner.argv) != 1 || runner.argv[0] != canonicalEntry {
		t.Fatalf("runner did not receive canonical confined entrypoint: %v", runner.argv)
	}
}

type runtimeExitError int

func (e runtimeExitError) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e runtimeExitError) ExitCode() int { return int(e) }

type resultRunner struct{ err error }

func (r resultRunner) Run(context.Context, []string) ([]byte, error) {
	return []byte("result"), r.err
}

func TestRuntimeReportsSandboxExitStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "success", want: 0},
		{name: "process failure", err: fmt.Errorf("sandbox: %w", runtimeExitError(7)), want: 7},
		{name: "start failure", err: errors.New("unable to start"), want: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "run"), []byte("fixture"), 0o700); err != nil {
				t.Fatal(err)
			}
			runtime, err := NewRuntime(root, RuntimePolicy{Sandbox: resultRunner{err: test.err}, ApprovedResources: map[string]bool{ApprovalKey("hook", "approved"): true}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runtime.Run(context.Background(), Resource{Kind: "hook", Path: filepath.Join(root, "hook.json"), Manifest: &Manifest{ID: "approved", Entrypoint: "run"}}, Invocation{})
			if !errors.Is(err, test.err) || result.ExitCode != test.want || result.Output != "result" {
				t.Fatalf("result=%+v err=%v, want exit code %d and error %v", result, err, test.want, test.err)
			}
		})
	}
}
