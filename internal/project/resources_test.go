package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverResourcesAndMemory(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"agents", "skills", "hooks", "plugins", "steering"} {
		if err := os.MkdirAll(filepath.Join(root, ".rsch", dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "agents", "backend.json"), []byte(`{"id":"backend","name":"Backend","version":"1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "skills", "go.md"), []byte("# Go"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rsch.md"), []byte("# Rules"), 0600); err != nil {
		t.Fatal(err)
	}
	resources, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Agents) != 1 || len(resources.Skills) != 1 || len(resources.Memory) != 1 {
		t.Fatalf("resources=%+v", resources)
	}
}
