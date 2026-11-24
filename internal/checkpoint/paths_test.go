package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreRejectsRedirectedWorkspacePath(t *testing.T) {
	for _, parentLink := range []bool{false, true} {
		name := "file"
		if parentLink {
			name = "parent"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			original := filepath.Join(root, "original", "a.txt")
			other := filepath.Join(root, "other", "a.txt")
			for _, path := range []string{original, other} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("original contents"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manager := NewManager(root)
			if _, err := manager.Create("redirected", []string{original}); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(original); err != nil {
				t.Fatal(err)
			}
			link, target := original, other
			if parentLink {
				link, target = filepath.Dir(original), filepath.Dir(other)
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := os.WriteFile(other, []byte("unrelated user contents"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := manager.RestoreWithApproval("redirected", true); err == nil {
				t.Fatal("restore followed a redirected workspace path")
			}
			data, err := os.ReadFile(other)
			if err != nil || string(data) != "unrelated user contents" {
				t.Fatalf("unrelated file changed: %q, error = %v", data, err)
			}
		})
	}
}

func TestCreateRejectsSymlinkFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewManager(root).Create("symlink", []string{link}); err == nil {
		t.Fatal("checkpoint accepted a symbolic link")
	}
}

func TestCheckpointStorageCannotEscapeWorkspace(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".rsch")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manager := NewManager(root)
	if _, err := manager.Create("escaped", []string{"absent.txt"}); err == nil {
		t.Fatal("checkpoint was created outside the workspace")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory changed: %v, error = %v", entries, err)
	}
}

func TestRestoreRejectsCheckpointDataEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root)
	if _, err := manager.Create("escape", []string{path}); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(manager.Root, "escape", "files")
	if err := os.Rename(filepath.Join(dataDir, "a.txt"), filepath.Join(outside, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dataDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(path, []byte("user contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreWithApproval("escape", true); err == nil {
		t.Fatal("restore read checkpoint contents outside its directory")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "user contents" {
		t.Fatalf("workspace file changed: %q, error = %v", data, err)
	}
}
