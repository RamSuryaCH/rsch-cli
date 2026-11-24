package integration_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/diff"
)

func TestPatchTransactionRollsBackRealFilesystemAfterPartialFailure(t *testing.T) {
	root := t.TempDir()
	addedPath := filepath.Join(root, "a-added.txt")
	updatedPath := filepath.Join(root, "b-updated.txt")
	failedPath := filepath.Join(root, "c-failed.txt")
	oldUpdated, newUpdated := "before-b", "after-b"
	oldFailed, newFailed := "before-c", "after-c"
	added := "new-a"
	if err := os.WriteFile(updatedPath, []byte(oldUpdated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failedPath, []byte(oldFailed), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := diff.Commit{Changes: map[string]diff.FileChange{
		addedPath:   {Type: diff.ActionAdd, NewContent: &added},
		updatedPath: {Type: diff.ActionUpdate, OldContent: &oldUpdated, NewContent: &newUpdated},
		failedPath:  {Type: diff.ActionUpdate, OldContent: &oldFailed, NewContent: &newFailed},
	}}
	failOnce := true
	err := diff.ApplyCommit(commit, func(path, content string) error {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
		if path == failedPath && content == newFailed && failOnce {
			failOnce = false
			return errors.New("injected commit failure")
		}
		return nil
	}, os.Remove)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("ApplyCommit error = %v, want rollback evidence", err)
	}
	if _, err := os.Stat(addedPath); !os.IsNotExist(err) {
		t.Fatalf("added file survived rollback: %v", err)
	}
	for path, want := range map[string]string{updatedPath: oldUpdated, failedPath: oldFailed} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != want {
			t.Fatalf("%s after rollback = %q, %v; want %q", path, content, readErr, want)
		}
	}
}
