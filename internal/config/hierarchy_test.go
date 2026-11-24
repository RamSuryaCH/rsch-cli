package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGovernanceAllowlist(t *testing.T) {
	if !approved(nil, "model") || !approved([]string{"model"}, "model") || approved([]string{"other"}, "model") {
		t.Fatal("unexpected governance allowlist result")
	}
}

func TestLoadHierarchyUsesHigherPrecedence(t *testing.T) {
	root := t.TempDir()
	low := filepath.Join(root, "global.json")
	high := filepath.Join(root, "repo.json")
	if err := os.WriteFile(low, []byte(`{"autonomy":"manual","policy":{"productionDenied":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(high, []byte(`{"autonomy":"assisted","policy":{"commandAllow":["go"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadHierarchy(low, high)
	if err != nil {
		t.Fatal(err)
	}
	if result["autonomy"] != "assisted" {
		t.Fatalf("result=%v", result)
	}
	policy := result["policy"].(map[string]any)
	if policy["productionDenied"] != true || policy["commandAllow"].([]any)[0] != "go" {
		t.Fatalf("policy=%v", policy)
	}
}

func TestHierarchyIncludesSessionAndCommandOverrides(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "session.json")
	command := filepath.Join(root, "command.json")
	if err := os.WriteFile(session, []byte(`{"autonomy":"assisted"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte(`{"autonomy":"manual"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RSCH_SESSION_CONFIG", session)
	t.Setenv("RSCH_COMMAND_CONFIG", command)
	paths := HierarchyPaths(root)
	if len(paths) != 6 || paths[4] != session || paths[5] != command {
		t.Fatalf("unexpected hierarchy paths: %v", paths)
	}
	merged, err := LoadHierarchy(paths...)
	if err != nil {
		t.Fatal(err)
	}
	if merged["autonomy"] != "manual" {
		t.Fatalf("command override was not highest precedence: %#v", merged)
	}
}

func TestResolveDataDirectoryUsesWorkspaceForRelativePaths(t *testing.T) {
	if got := ResolveDataDirectory("/workspace/project", "state"); got != filepath.Join("/workspace/project", "state") {
		t.Fatalf("relative data directory = %q", got)
	}
	if got := ResolveDataDirectory("/workspace/project", "/var/lib/rsch"); got != filepath.Clean("/var/lib/rsch") {
		t.Fatalf("absolute data directory = %q", got)
	}
	if got := ResolveDataDirectory("/workspace/project", ""); got != filepath.Join("/workspace/project", ".rsch") {
		t.Fatalf("default data directory = %q", got)
	}
}
