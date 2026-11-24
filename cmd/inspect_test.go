package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWritableOrCreatableAcceptsMissingChild(t *testing.T) {
	root := t.TempDir()
	if !isWritableOrCreatable(filepath.Join(root, "state", "cache")) {
		t.Fatal("expected a child of a writable workspace to be creatable")
	}
}

func TestIsWritableOrCreatableRejectsFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isWritableOrCreatable(file) {
		t.Fatal("expected a regular file to fail the directory check")
	}
}
