package steering

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrdersRulesByExplicitPrecedence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".rsch", "steering")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repository.md"), []byte("repo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task-release.md"), []byte("task"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	local := []Rule{}
	for _, rule := range set.Rules {
		if filepath.IsAbs(rule.Path) && len(rule.Path) >= len(root) && rule.Path[:len(root)] == root {
			local = append(local, rule)
		}
	}
	if len(local) != 2 || local[0].Scope != Repository || local[1].Scope != Task {
		t.Fatalf("unexpected rule order: %+v", local)
	}
	if set.Combined == "" {
		t.Fatal("expected combined rules")
	}
}

func TestLoadIncludesNestedDirectoryRules(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "src", ".rsch", "steering")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "directory.md"), []byte("directory rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rule := range set.Rules {
		if rule.Name == "directory.md" && rule.Scope == Directory {
			found = true
		}
	}
	if !found {
		t.Fatalf("nested directory rule not loaded: %+v", set.Rules)
	}
}

func TestLoadRejectsSymlinkedRulesOutsideWorkspace(t *testing.T) {
	for _, linkDirectory := range []bool{false, true} {
		name := "file"
		if linkDirectory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			outsideFile := filepath.Join(outside, "secret.md")
			if err := os.WriteFile(outsideFile, []byte("private external content"), 0o600); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, ".rsch", "steering")
			link, target := filepath.Join(directory, "secret.md"), outsideFile
			if linkDirectory {
				link, target = directory, outside
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			set, err := Load(root)
			if err == nil {
				t.Fatalf("loaded external steering content through a symlink: %+v", set)
			}
		})
	}
}

func TestLoadAllowsRuleLinksWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".rsch", "steering")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared.md"), []byte("shared repository rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "repository-linked.md")
	if err := os.Symlink(filepath.Join("..", "..", "shared.md"), path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range set.Rules {
		if rule.Path == path && rule.Content == "shared repository rule" && rule.Scope == Repository {
			return
		}
	}
	t.Fatalf("workspace rule link was not loaded: %+v", set.Rules)
}
