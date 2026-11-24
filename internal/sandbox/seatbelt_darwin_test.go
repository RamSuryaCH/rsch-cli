//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeatbeltNativeIsolation(t *testing.T) {
	capability := Detect()
	if capability.Backend != "seatbelt" || !capability.Isolated {
		t.Skipf("native Seatbelt is unavailable: %s", capability.Reason)
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	fakeHome := filepath.Join(root, "home")
	for _, path := range []string{
		workspace,
		filepath.Join(workspace, ".git"),
		filepath.Join(workspace, ".rsch"),
		filepath.Join(fakeHome, ".ssh"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	const insideMarker = "seatbelt-workspace-marker"
	const outsideMarker = "seatbelt-outside-secret"
	const homeMarker = "seatbelt-home-secret"
	insidePath := filepath.Join(workspace, "inside.txt")
	outsidePath := filepath.Join(root, "outside-secret.txt")
	homeSecretPath := filepath.Join(fakeHome, ".ssh", "id_test")
	for path, content := range map[string]string{
		insidePath:     insideMarker,
		outsidePath:    outsideMarker,
		homeSecretPath: homeMarker,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	symlinkPath := filepath.Join(workspace, "outside-link")
	if err := os.Symlink(outsidePath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	readOnly := PlatformRunner{Capability: capability, WorkingDirectory: workspace, Mode: ReadOnly, AllowNetwork: false}
	writable := PlatformRunner{Capability: capability, WorkingDirectory: workspace, Mode: WorkspaceWrite, AllowNetwork: false}

	if output, err := runSeatbeltTestCommand(t, readOnly, []string{"/usr/bin/true"}, nil); err != nil {
		t.Fatalf("native executable bootstrap failed: output=%q err=%v", output, err)
	}
	output, err := runSeatbeltTestCommand(t, readOnly, []string{"/bin/cat", insidePath}, nil)
	if err != nil || string(output) != insideMarker {
		t.Fatalf("workspace read failed: output=%q err=%v", output, err)
	}
	// A coding command commonly spawns its own compiler, test, or helper
	// processes. Descendants inherit the same Seatbelt profile, so allow the
	// fork while proving the child remains inside the established policy.
	output, err = runSeatbeltTestCommand(t, readOnly, []string{"/bin/sh", "-c", `/bin/cat "$1"; :`, "seatbelt-test", insidePath}, nil)
	if err != nil || !strings.Contains(string(output), insideMarker) {
		t.Fatalf("sandboxed shell child failed: output=%q err=%v", output, err)
	}

	for name, testCase := range map[string]struct {
		path        string
		marker      string
		environment map[string]string
	}{
		"sibling": {path: outsidePath, marker: outsideMarker},
		"home": {
			path:        homeSecretPath,
			marker:      homeMarker,
			environment: map[string]string{"HOME": fakeHome},
		},
		"symlink escape": {path: symlinkPath, marker: outsideMarker},
	} {
		t.Run("deny read "+name, func(t *testing.T) {
			output, err := runSeatbeltTestCommand(t, readOnly, []string{"/bin/cat", testCase.path}, testCase.environment)
			if err == nil {
				t.Fatalf("outside-workspace read unexpectedly succeeded: %q", output)
			}
			if strings.Contains(string(output), testCase.marker) {
				t.Fatalf("outside-workspace content leaked in command output: %q", output)
			}
		})
	}

	readOnlyWrite := filepath.Join(workspace, "read-only-write")
	if output, err := runSeatbeltTestCommand(t, readOnly, []string{"/usr/bin/touch", readOnlyWrite}, nil); err == nil {
		t.Fatalf("read-only workspace write unexpectedly succeeded: %q", output)
	}
	assertSeatbeltPathAbsent(t, readOnlyWrite)

	writablePath := filepath.Join(workspace, "write-ok")
	if output, err := runSeatbeltTestCommand(t, writable, []string{"/usr/bin/touch", writablePath}, nil); err != nil {
		t.Fatalf("workspace write failed: output=%q err=%v", output, err)
	}
	if _, err := os.Stat(writablePath); err != nil {
		t.Fatalf("workspace write did not create its target: %v", err)
	}

	for name, path := range map[string]string{
		"sibling": filepath.Join(root, "outside-write"),
		"git":     filepath.Join(workspace, ".git", "config.new"),
		"rsch":    filepath.Join(workspace, ".rsch", "state.new"),
	} {
		t.Run("deny write "+name, func(t *testing.T) {
			if output, err := runSeatbeltTestCommand(t, writable, []string{"/usr/bin/touch", path}, nil); err == nil {
				t.Fatalf("protected write unexpectedly succeeded: %q", output)
			}
			assertSeatbeltPathAbsent(t, path)
		})
	}

	address := seatbeltTestListenerAddress(t)
	networkEnabled := PlatformRunner{Capability: capability, WorkingDirectory: workspace, Mode: ReadOnly, AllowNetwork: true}
	if output, err := runSeatbeltTestCommand(t, networkEnabled, seatbeltNetcatCommand(address), nil); err != nil {
		t.Fatalf("network-enabled control failed: output=%q err=%v", output, err)
	}

	address = seatbeltTestListenerAddress(t)
	if output, err := runSeatbeltTestCommand(t, readOnly, seatbeltNetcatCommand(address), nil); err == nil {
		t.Fatalf("network-disabled connection unexpectedly succeeded: %q", output)
	}
}

func TestSeatbeltSymlinkedWorkspace(t *testing.T) {
	capability := Detect()
	if capability.Backend != "seatbelt" || !capability.Isolated {
		t.Skipf("native Seatbelt is unavailable: %s", capability.Reason)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte("workspace-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := PlatformRunner{Capability: capability, WorkingDirectory: alias, Mode: ReadOnly}
	output, err := runSeatbeltTestCommand(t, runner, []string{"/bin/cat", "inside.txt"}, nil)
	if err != nil || string(output) != "workspace-data" {
		t.Fatalf("symlinked workspace read failed: output=%q err=%v", output, err)
	}
}

func TestSeatbeltSymlinkedControlDirectoriesRemainReadOnly(t *testing.T) {
	capability := Detect()
	if capability.Backend != "seatbelt" || !capability.Isolated {
		t.Skipf("native Seatbelt is unavailable: %s", capability.Reason)
	}
	for _, name := range []string{".git", ".rsch"} {
		t.Run(name, func(t *testing.T) {
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(workspace, "control-data")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(workspace, name)
			if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			runner := PlatformRunner{Capability: capability, WorkingDirectory: workspace, Mode: WorkspaceWrite}
			for _, path := range []string{filepath.Join(alias, "forbidden"), filepath.Join(target, "forbidden")} {
				if output, err := runSeatbeltTestCommand(t, runner, []string{"/usr/bin/touch", path}, nil); err == nil {
					t.Errorf("protected control-directory write succeeded: path=%q output=%q", path, output)
				}
			}
			assertSeatbeltPathAbsent(t, filepath.Join(target, "forbidden"))
		})
	}
}

func TestSeatbeltRejectsUnresolvedControlDirectoryLinks(t *testing.T) {
	for _, target := range []string{"missing-control-data", ".rsch"} {
		t.Run(target, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.Symlink(target, filepath.Join(workspace, ".rsch")); err != nil {
				t.Fatal(err)
			}
			if _, err := seatbeltCommand(workspace, WorkspaceWrite, false, []string{"/usr/bin/true"}); err == nil {
				t.Fatal("expected unresolved protected path to prevent sandbox execution")
			}
		})
	}
}

func runSeatbeltTestCommand(t *testing.T, runner PlatformRunner, argv []string, environment map[string]string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if environment != nil {
		return runner.RunWithEnvironment(ctx, argv, environment)
	}
	return runner.Run(ctx, argv)
}

func assertSeatbeltPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("denied target exists at %q (stat error: %v)", path, err)
	}
}

func seatbeltTestListenerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String()
}

func seatbeltNetcatCommand(address string) []string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		panic(fmt.Sprintf("invalid test listener address %q: %v", address, err))
	}
	return []string{"/usr/bin/nc", "-4", "-z", "-w", "2", host, port}
}
