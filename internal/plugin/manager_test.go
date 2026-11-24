package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/project"
)

type testRunner struct{ called bool }

func (r *testRunner) Run(_ context.Context, argv []string) ([]byte, error) {
	r.called = true
	return []byte(argv[0]), nil
}

func TestInstallListInspectAndRemove(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(`{"capabilities":["commands"],"entrypoint":"run.sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := m.Install("demo", source)
	if err != nil {
		t.Fatal(err)
	}
	if mode := mustStat(t, path).Mode().Perm(); mode != 0o600 {
		t.Fatalf("plugin permissions=%o", mode)
	}
	items, err := m.List()
	if err != nil || len(items) != 1 || items[0].APIVersion != APIVersion {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if _, err := m.Inspect("../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if err := m.Remove("demo"); err != nil {
		t.Fatal(err)
	}
}

func TestRunUsesProjectRuntime(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(`{"entrypoint":"run.sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install("demo", source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "plugins", "run.sh"), []byte("ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &testRunner{}
	runtime, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("plugin", "demo"): true}, Sandbox: runner})
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Run(context.Background(), "demo", runtime, project.Invocation{})
	if err != nil || !runner.called || result.Kind != "plugin" {
		t.Fatalf("result=%+v err=%v called=%v", result, err, runner.called)
	}
}

func TestInvokeValidatesDeclaredCapabilityBeforeRuntime(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(`{"capabilities":["commands"],"entrypoint":"run.sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install("demo", source); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, ".rsch", "plugins", "run.sh")
	if err := os.WriteFile(entry, []byte("ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &testRunner{}
	runtime, err := project.NewRuntime(root, project.RuntimePolicy{ApprovedResources: map[string]bool{project.ApprovalKey("plugin", "demo"): true}, Sandbox: runner, AllowedCapabilities: map[string]bool{"commands": true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Invoke(context.Background(), "demo", "tools", runtime, nil, ""); err == nil || runner.called {
		t.Fatalf("unsupported extension was dispatched: err=%v called=%v", err, runner.called)
	}
	if _, err := m.Invoke(context.Background(), "demo", "commands", runtime, []string{"--check"}, "{}"); err != nil || !runner.called {
		t.Fatalf("declared extension failed: err=%v called=%v", err, runner.called)
	}
}

func TestPluginManifestValidationAndDescriptor(t *testing.T) {
	definition := project.Manifest{ID: "p", Name: "Plugin", Version: "1", Entrypoint: "run", Capabilities: []string{"tools", "commands"}, Permissions: []string{"filesystem.read"}}
	if err := ValidateManifest(definition); err != nil {
		t.Fatal(err)
	}
	descriptor := Describe(definition)
	if descriptor.APIVersion != APIVersion || len(descriptor.Extensions) != 2 || descriptor.Extensions[0].Kind != "commands" {
		t.Fatalf("unexpected descriptor: %+v", descriptor)
	}
	definition.Capabilities = []string{"unknown-capability"}
	if err := ValidateManifest(definition); err == nil {
		t.Fatal("expected unknown capability rejection")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestRegistryOperationsStayInsideWorkspace(t *testing.T) {
	for _, linkedDirectory := range []string{".rsch", filepath.Join(".rsch", "plugins")} {
		t.Run(linkedDirectory, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(root, linkedDirectory)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			source := filepath.Join(root, "source.json")
			if err := os.WriteFile(source, []byte(`{"entrypoint":"run.sh"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			manager, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Install("demo", source); err == nil {
				t.Fatal("install followed a catalog symlink outside the workspace")
			}
			if _, err := manager.List(); err == nil {
				t.Fatal("list followed a catalog symlink outside the workspace")
			}
			if _, err := manager.Inspect("demo"); err == nil {
				t.Fatal("inspect followed a catalog symlink outside the workspace")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside directory changed: %v, %v", entries, err)
			}
		})
	}
}

func TestInstallRejectsDanglingManifestSymlink(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".rsch", "plugins")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.Symlink(target, filepath.Join(directory, "demo.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(`{"entrypoint":"run.sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install("demo", source); err == nil {
		t.Fatal("install must reject an existing dangling symlink")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("install wrote outside the catalog: %v", err)
	}
}

func TestInspectRejectsExternalManifestSymlink(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(manager.Root, ".rsch", "plugins")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(target, []byte(`{"id":"demo","name":"demo","version":"1","entrypoint":"run.sh"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "demo.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := manager.Inspect("demo"); err == nil {
		t.Fatal("inspect read a manifest outside the catalog")
	}
	if _, err := manager.List(); err == nil {
		t.Fatal("list read a manifest outside the catalog")
	}
}

func TestInstallRejectsUnusableManifestFields(t *testing.T) {
	for _, manifest := range []string{
		`{"entrypoint":" "}`,
		`{"id":" ","entrypoint":"run.sh"}`,
		`{"name":" ","entrypoint":"run.sh"}`,
		`{"version":" ","entrypoint":"run.sh"}`,
		`{"capabilities":[" commands "],"entrypoint":"run.sh"}`,
	} {
		t.Run(manifest, func(t *testing.T) {
			manager, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "source.json")
			if err := os.WriteFile(source, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Install("demo", source); err == nil {
				t.Fatal("invalid plugin manifest was installed")
			}
		})
	}
}
