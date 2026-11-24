package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspacePathRejectsSiblingPrefixAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if _, err := ResolveWorkspacePath(root, filepath.Join(root+"-other", "file.txt")); err == nil {
		t.Fatal("sibling-prefix path was accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveWorkspacePath(root, filepath.Join(link, "secret.txt")); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestResolveWorkspacePathAcceptsNewWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	path, err := ResolveWorkspacePath(root, filepath.Join(root, "nested", "file.txt"))
	canonicalRoot, _ := filepath.EvalSymlinks(root)
	expected := filepath.Join(canonicalRoot, "nested", "file.txt")
	if err != nil || path != expected {
		t.Fatalf("unexpected workspace path: %q %v", path, err)
	}
}

func TestResolveWorkspacePathRejectsDanglingSymlinkAncestors(t *testing.T) {
	root := t.TempDir()
	for name, destination := range map[string]string{
		"outside": filepath.Join(t.TempDir(), "missing"),
		"inside":  filepath.Join(root, "missing"),
	} {
		t.Run(name, func(t *testing.T) {
			link := filepath.Join(root, name)
			if err := os.Symlink(destination, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if path, err := ResolveWorkspacePath(root, filepath.Join(link, "nested", "file.txt")); err == nil {
				t.Fatalf("unresolvable symlink ancestor was accepted: %q", path)
			}
		})
	}
}
