package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/sandbox"
)

func TestLSPClientStartOptionsRejectsUntrustedWorkspace(t *testing.T) {
	_, err := lspClientStartOptions(&config.Config{}, sandbox.Capability{}, t.TempDir(), "gopls")
	if !errors.Is(err, errUntrustedLSPWorkspace) {
		t.Fatalf("error = %v, want untrusted workspace", err)
	}
}

func TestLSPClientStartOptionsRequiresConfiguredIsolation(t *testing.T) {
	cfg := &config.Config{WorkspaceTrusted: true, Policy: config.PolicyConfig{RequireLSPIsolation: true}}
	_, err := lspClientStartOptions(cfg, sandbox.Capability{State: sandbox.RequiredButMissing, Reason: "missing"}, t.TempDir(), "gopls")
	if !errors.Is(err, errLSPIsolationRequired) {
		t.Fatalf("error = %v, want required isolation failure", err)
	}
}

func TestLSPClientStartOptionsUsesAvailableSandbox(t *testing.T) {
	workspace := t.TempDir()
	capability := sandbox.Capability{
		State: sandbox.Available, Isolated: true, Supported: true,
		FilesystemIsolation: true, NetworkIsolation: true, ProcessIsolation: true,
		Backend: "bubblewrap",
	}
	options, err := lspClientStartOptions(&config.Config{WorkspaceTrusted: true}, capability, workspace, "gopls", "serve")
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Argv) == 0 || options.Argv[0] != "bwrap" {
		t.Fatalf("sandbox argv = %v", options.Argv)
	}
	if !containsString(options.Argv, "--unshare-net") || !reflect.DeepEqual(options.Argv[len(options.Argv)-2:], []string{"gopls", "serve"}) {
		t.Fatalf("sandbox did not preserve network policy/command: %v", options.Argv)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
