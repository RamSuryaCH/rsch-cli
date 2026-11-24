package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestPlatformRunnerRejectsFirejailIsolationClaims(t *testing.T) {
	for _, mode := range []Mode{ReadOnly, WorkspaceWrite, FullAccess} {
		t.Run(string(mode), func(t *testing.T) {
			runner := PlatformRunner{Capability: Capability{
				Backend: "firejail", Supported: true, Isolated: true,
				FilesystemIsolation: true, NetworkIsolation: true, ProcessIsolation: true,
			}, Mode: mode}
			if _, err := runner.Wrap([]string{"true"}); !errors.Is(err, ErrInsufficientCapability) {
				t.Fatalf("incomplete firejail isolation was accepted: %v", err)
			}
		})
	}
}

func TestSandboxEnvironmentHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "sandbox-environment-helper" {
		return
	}
	fmt.Print(os.Getenv("RSCH_SANDBOX_PARENT_SECRET"))
	fmt.Print(os.Getenv("RSCH_SANDBOX_EXPLICIT_VALUE"))
	os.Exit(0)
}

func TestPlatformRunnerNeverInheritsCallerSecrets(t *testing.T) {
	t.Setenv("RSCH_SANDBOX_PARENT_SECRET", "synthetic-parent-secret")
	t.Setenv("RSCH_WINDOWS_SANDBOX_COMMAND", os.Args[0])
	runner := PlatformRunner{Capability: Capability{
		Backend: "windows-configured-wrapper", Supported: true, Isolated: true,
		FilesystemIsolation: true, NetworkIsolation: true, ProcessIsolation: true,
	}, Mode: ReadOnly, WorkingDirectory: t.TempDir()}
	argv := []string{"-test.run=^TestSandboxEnvironmentHelper$", "--", "sandbox-environment-helper"}
	for _, testCase := range []struct {
		name        string
		environment map[string]string
		want        string
	}{
		{name: "nil"},
		{name: "empty", environment: map[string]string{}},
		{name: "explicit", environment: map[string]string{"RSCH_SANDBOX_EXPLICIT_VALUE": "explicit-value"}, want: "explicit-value"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := runner.RunWithEnvironment(context.Background(), argv, testCase.environment)
			if err != nil || string(output) != testCase.want {
				t.Fatalf("unexpected sandbox environment: output=%q err=%v", output, err)
			}
		})
	}
	if output, err := runner.Run(context.Background(), argv); err != nil || len(output) != 0 {
		t.Fatalf("Run inherited the parent environment: output=%q err=%v", output, err)
	}
}

func TestPlatformRunnerFailsClosedWhenIsolationUnavailable(t *testing.T) {
	runner := PlatformRunner{Capability: Capability{Backend: "windows-job", Isolated: false}}
	_, err := runner.Run(context.Background(), []string{"echo", "unsafe"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestPlatformRunnerRejectsEmptyCommand(t *testing.T) {
	runner := PlatformRunner{Capability: Capability{Backend: "seatbelt", Isolated: true}}
	_, err := runner.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected empty command error")
	}
}

func TestPlatformRunnerRejectsIncompleteIsolationCapability(t *testing.T) {
	runner := PlatformRunner{Capability: Capability{
		Backend:             "seatbelt",
		Supported:           true,
		Isolated:            true,
		FilesystemIsolation: true,
		ProcessIsolation:    true,
		// A network-capable but non-isolating backend cannot satisfy the
		// sandbox contract, even when the caller only supplies a harmless argv.
		NetworkIsolation: false,
	}, Mode: ReadOnly}
	_, err := runner.Run(context.Background(), []string{"true"})
	if !errors.Is(err, ErrInsufficientCapability) {
		t.Fatalf("expected incomplete capability rejection, got %v", err)
	}
}

func TestPlatformRunnerRejectsUnknownMode(t *testing.T) {
	runner := PlatformRunner{Capability: Capability{
		Backend:             "seatbelt",
		Supported:           true,
		Isolated:            true,
		FilesystemIsolation: true,
		NetworkIsolation:    true,
		ProcessIsolation:    true,
	}, Mode: Mode("unrestricted")}
	_, err := runner.Run(context.Background(), []string{"true"})
	if !errors.Is(err, ErrInsufficientCapability) {
		t.Fatalf("expected unknown mode rejection, got %v", err)
	}
}

func TestSeatbeltProfileKeepsProjectDataAndNetworkScoped(t *testing.T) {
	workspace := filepath.Join(string(filepath.Separator), "Users", "developer", "project with spaces")
	quotedWorkspace := strconv.Quote(workspace)

	readOnly := seatbeltProfile(workspace, ReadOnly, false)
	for _, required := range []string{
		`(deny default)`,
		`(allow process-fork)`,
		`(allow file-read-data (literal "/"))`,
		fmt.Sprintf("(allow file-read* (subpath %s))", quotedWorkspace),
		fmt.Sprintf("(deny file-write* (subpath %s))", strconv.Quote(filepath.Join(workspace, ".git"))),
		fmt.Sprintf("(deny file-write* (subpath %s))", strconv.Quote(filepath.Join(workspace, ".rsch"))),
		`(deny network*)`,
	} {
		if !strings.Contains(readOnly, required) {
			t.Fatalf("read-only profile is missing %q: %s", required, readOnly)
		}
	}
	for _, forbidden := range []string{
		`(allow file-read*)`,
		`(allow file-write*)`,
		`(allow network*)`,
		fmt.Sprintf("(allow file-write* (subpath %s))", quotedWorkspace),
	} {
		if strings.Contains(readOnly, forbidden) {
			t.Fatalf("read-only profile contains unscoped or excessive grant %q: %s", forbidden, readOnly)
		}
	}

	writable := seatbeltProfile(workspace, WorkspaceWrite, true)
	for _, required := range []string{
		fmt.Sprintf("(allow file-read* (subpath %s))", quotedWorkspace),
		fmt.Sprintf("(allow file-write* (subpath %s))", quotedWorkspace),
		`(allow network*)`,
	} {
		if !strings.Contains(writable, required) {
			t.Fatalf("writable profile is missing %q: %s", required, writable)
		}
	}
	if strings.Contains(writable, `(deny network*)`) {
		t.Fatalf("network-enabled profile still denies the network: %s", writable)
	}
}

func TestBubblewrapFilesystemViewExcludesHostRootAndHome(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{".git", ".rsch"} {
		if err := os.Mkdir(filepath.Join(workspace, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := PlatformRunner{
		Capability: Capability{
			Backend:             "bubblewrap",
			Supported:           true,
			Isolated:            true,
			FilesystemIsolation: true,
			NetworkIsolation:    true,
			ProcessIsolation:    true,
		},
		WorkingDirectory: workspace,
		Mode:             WorkspaceWrite,
		AllowNetwork:     false,
	}
	command, err := runner.Wrap([]string{"/bin/sh", "-c", "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(command, "--unshare-net") {
		t.Fatalf("bubblewrap command did not isolate the network: %#v", command)
	}

	mounts := make(map[string]string)
	for index := 0; index+2 < len(command); index++ {
		if command[index] == "--bind" || command[index] == "--ro-bind" {
			mounts[command[index+1]] = command[index]
			index += 2
		}
	}
	if _, exposed := mounts["/"]; exposed {
		t.Fatalf("bubblewrap exposed the host root: %#v", command)
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		if _, exposed := mounts[home]; exposed {
			t.Fatalf("bubblewrap exposed the user home %q", home)
		}
	}
	if mounts[workspace] != "--bind" {
		t.Fatalf("workspace is not the sole writable data mount: %#v", command)
	}
	for _, protected := range []string{filepath.Join(workspace, ".git"), filepath.Join(workspace, ".rsch")} {
		if mounts[protected] != "--ro-bind" {
			t.Fatalf("protected workspace path %q is not read-only: %#v", protected, command)
		}
	}
	for source, kind := range mounts {
		if kind == "--bind" && source != workspace {
			t.Fatalf("unexpected writable host mount %q: %#v", source, command)
		}
	}
}

func TestBubblewrapDeniesOutsideWorkspaceSecret(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap integration requires Linux")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	insidePath := filepath.Join(workspace, "inside.txt")
	secretPath := filepath.Join(root, "outside-secret.txt")
	if err := os.WriteFile(insidePath, []byte("workspace-visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte("credential-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	capability := Probe(context.Background(), workspace)
	if !capability.Isolated || capability.Backend != "bubblewrap" {
		t.Skipf("functional bubblewrap is unavailable: %s", capability.Reason)
	}
	runner := PlatformRunner{Capability: capability, WorkingDirectory: workspace, Mode: ReadOnly, AllowNetwork: false}
	output, err := runner.Run(context.Background(), []string{"/bin/cat", insidePath})
	if err != nil || string(output) != "workspace-visible" {
		t.Fatalf("workspace read failed: output=%q err=%v", output, err)
	}
	output, err = runner.Run(context.Background(), []string{"/bin/cat", secretPath})
	if err == nil {
		t.Fatalf("outside-workspace secret was readable: %q", output)
	}
	if strings.Contains(string(output), "credential-secret") {
		t.Fatalf("outside-workspace secret leaked in output: %q", output)
	}
}
