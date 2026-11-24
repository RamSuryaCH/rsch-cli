package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestWorktreeStorageCannotEscapeWorkspace(t *testing.T) {
	if !process.Exists("git") {
		t.Skip("git is unavailable")
	}
	for _, fork := range []bool{false, true} {
		name := "clean"
		if fork {
			name = "fork"
		}
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			gitTestCommand(t, root, "init", "-q")
			gitTestCommand(t, root, "config", "user.email", "rsch-test@example.invalid")
			gitTestCommand(t, root, "config", "user.name", "RSCH Test")
			if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".rsch\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitTestCommand(t, root, "add", ".gitignore")
			gitTestCommand(t, root, "commit", "-qm", "base")
			if err := os.Symlink(outside, filepath.Join(root, ".rsch")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			manager := NewManager(root)
			var err error
			if fork {
				if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("untracked"), 0o600); err != nil {
					t.Fatal(err)
				}
				_, err = manager.CreateFork(context.Background(), root, "escaped")
			} else {
				_, err = manager.Create(context.Background(), root, "escaped")
			}
			if err == nil {
				t.Fatal("worktree was created outside the workspace")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("outside directory changed: %v, error = %v", entries, err)
			}
		})
	}
}

func TestCopyForkPathRejectsSymlinkEscapes(t *testing.T) {
	for _, linkAt := range []string{"source-parent", "target-parent", "target-file"} {
		t.Run(linkAt, func(t *testing.T) {
			root, fork, outside := t.TempDir(), t.TempDir(), t.TempDir()
			source := filepath.Join(root, "dir", "a.txt")
			target := filepath.Join(fork, "dir", "a.txt")
			external := filepath.Join(outside, "a.txt")
			for _, path := range []string{source, target} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(source, []byte("source contents"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(external, []byte("outside contents"), 0o600); err != nil {
				t.Fatal(err)
			}
			link, destination := target, external
			switch linkAt {
			case "source-parent":
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				link, destination = filepath.Dir(source), outside
			case "target-parent":
				link, destination = filepath.Dir(target), outside
			}
			if linkAt != "target-file" {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(destination, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := copyForkPath(root, fork, "dir/a.txt"); err == nil {
				t.Fatal("fork copy followed a symlink outside its workspace")
			}
			data, err := os.ReadFile(external)
			if err != nil || string(data) != "outside contents" {
				t.Fatalf("outside contents changed: %q, error = %v", data, err)
			}
		})
	}
}

func TestCopyForkPathPreservesSymlink(t *testing.T) {
	root, fork, outside := t.TempDir(), t.TempDir(), t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := copyForkPath(root, fork, "link"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(fork, "link"))
	if err != nil || target != outside {
		t.Fatalf("copied link = %q, error = %v", target, err)
	}
}
