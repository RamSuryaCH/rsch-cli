package customagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/project"
)

func TestCustomAgentLifecycle(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := manager.Create("reviewer", project.Manifest{Role: "independent reviewer", Instructions: "Review safely", Risk: "high", Tools: []string{"view"}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest permissions: %v %v", info, err)
	}
	entry, err := manager.Inspect("reviewer")
	if err != nil || entry.Definition.Role != "independent reviewer" {
		t.Fatalf("unexpected agent: %+v %v", entry, err)
	}
	if _, err := manager.Create("reviewer", project.Manifest{Role: "duplicate"}); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	if err := manager.Remove("reviewer"); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryOperationsStayInsideWorkspace(t *testing.T) {
	for _, linkedDirectory := range []string{".rsch", filepath.Join(".rsch", "agents")} {
		t.Run(linkedDirectory, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(root, linkedDirectory)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			manager, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Create("demo", project.Manifest{Role: "reviewer"}); err == nil {
				t.Fatal("create followed a catalog symlink outside the workspace")
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

func TestCreateRejectsDanglingManifestSymlink(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".rsch", "agents")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.Symlink(target, filepath.Join(directory, "demo.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manager, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create("demo", project.Manifest{Role: "reviewer"}); err == nil {
		t.Fatal("create must reject an existing dangling symlink")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("create wrote outside the catalog: %v", err)
	}
}

func TestInspectRejectsExternalManifestSymlink(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(manager.Root, ".rsch", "agents")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(target, []byte(`{"id":"demo","name":"demo","version":"1","role":"reviewer"}`), 0o600); err != nil {
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

func TestDeclarativeAgentDefinitionPersistsDelegationContract(t *testing.T) {
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.CreateDefinition("coordinator", Definition{
		Manifest: project.Manifest{
			Role:        "coordinator",
			Skills:      []string{"planning"},
			Tools:       []string{"view"},
			Permissions: []string{"read"},
		},
		Description:    "Coordinates bounded work",
		Subagents:      []string{"researcher", "reviewer"},
		Reviewers:      []string{"reviewer"},
		MaxDepth:       2,
		MaxAgents:      4,
		MaxConcurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := manager.Inspect("coordinator")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Definition.Description != "Coordinates bounded work" || len(entry.Definition.Subagents) != 2 || entry.Definition.MaxAgents != 4 {
		t.Fatalf("declarative contract was not preserved: %+v", entry.Definition)
	}
}

func TestDeclarativeAgentDefinitionRejectsUnsafeLimitsAndSelfDelegation(t *testing.T) {
	for _, definition := range []Definition{
		{Manifest: project.Manifest{Role: "role"}, Subagents: []string{"coordinator"}},
		{Manifest: project.Manifest{Role: "role"}, MaxDepth: -1},
		{Manifest: project.Manifest{Role: "role"}, MaxAgents: 1, MaxConcurrency: 2},
		{Manifest: project.Manifest{Role: "role", Budget: -1}},
		{Manifest: project.Manifest{Role: "role", Timeout: -1}},
		{Manifest: project.Manifest{Role: "role"}, Subagents: []string{" reviewer "}},
	} {
		t.Run("invalid", func(t *testing.T) {
			manager, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.CreateDefinition("coordinator", definition); err == nil {
				t.Fatalf("expected invalid declarative definition: %+v", definition)
			}
		})
	}
}
