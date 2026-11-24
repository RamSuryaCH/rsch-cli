package diff

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestApplyCommitUsesDeterministicPathOrder(t *testing.T) {
	oldA, newA := "old-a", "new-a"
	oldB, newB := "old-b", "new-b"
	commit := Commit{Changes: map[string]FileChange{
		"b.txt": {Type: ActionUpdate, OldContent: &oldB, NewContent: &newB},
		"a.txt": {Type: ActionUpdate, OldContent: &oldA, NewContent: &newA},
	}}
	order := make([]string, 0, 2)
	err := ApplyCommit(commit, func(path, _ string) error {
		order = append(order, path)
		return nil
	}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.txt", "b.txt"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("application order = %v, want %v", order, want)
	}
}

func TestApplyCommitRollsBackEveryPathAfterPartialWriteFailure(t *testing.T) {
	oldA, newA := "old-a", "new-a"
	oldB, newB := "old-b", "new-b"
	files := map[string]string{"a.txt": oldA, "b.txt": oldB}
	commit := Commit{Changes: map[string]FileChange{
		"a.txt": {Type: ActionUpdate, OldContent: &oldA, NewContent: &newA},
		"b.txt": {Type: ActionUpdate, OldContent: &oldB, NewContent: &newB},
	}}
	failOnce := true
	err := ApplyCommit(commit, func(path, content string) error {
		files[path] = content
		if path == "b.txt" && content == newB && failOnce {
			failOnce = false
			return errors.New("injected write failure")
		}
		return nil
	}, func(path string) error {
		delete(files, path)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("ApplyCommit error = %v, want rollback evidence", err)
	}
	if !reflect.DeepEqual(files, map[string]string{"a.txt": oldA, "b.txt": oldB}) {
		t.Fatalf("files after rollback = %#v", files)
	}
}

func TestApplyCommitRejectsMoveTargetThatIsAlsoChanged(t *testing.T) {
	oldA, newA := "old-a", "new-a"
	newB := "new-b"
	target := "b.txt"
	commit := Commit{Changes: map[string]FileChange{
		"a.txt": {Type: ActionUpdate, OldContent: &oldA, NewContent: &newA, MovePath: &target},
		"b.txt": {Type: ActionAdd, NewContent: &newB},
	}}
	called := false
	err := ApplyCommit(commit, func(string, string) error {
		called = true
		return nil
	}, func(string) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "also changed") {
		t.Fatalf("ApplyCommit error = %v, want move collision", err)
	}
	if called {
		t.Fatal("invalid commit mutated the filesystem")
	}
}

func TestTextToPatchRejectsUnknownUpdateDirective(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: file.txt\n*** Unknown Directive\n*** End Patch"
	if _, _, err := TextToPatch(patch, map[string]string{"file.txt": "before\n"}); err == nil {
		t.Fatal("unknown update directive must be rejected")
	}
}

func TestPatchToCommitRejectsOutOfBoundsDeletion(t *testing.T) {
	patch := Patch{Actions: map[string]PatchAction{
		"file.txt": {Type: ActionUpdate, Chunks: []Chunk{{OrigIndex: 1, DelLines: []string{"extra"}}}},
	}}
	if _, err := PatchToCommit(patch, map[string]string{"file.txt": "before"}); err == nil {
		t.Fatal("deletion beyond original content must be rejected")
	}
}

func TestParserReportsMissingTerminator(t *testing.T) {
	parser := NewParser(map[string]string{}, []string{"*** Add File: file.txt", "+content"})
	if err := parser.Parse(); err == nil {
		t.Fatal("missing terminator must produce an error")
	}
}

func TestProcessPatchAppliesValidUpdateAndAddition(t *testing.T) {
	files := map[string]string{"file.txt": "before\ncontext\n"}
	patch := "*** Begin Patch\n*** Update File: file.txt\n@@\n-before\n+after\n context\n*** Add File: added.txt\n+added\n*** End Patch"
	_, err := ProcessPatch(patch, func(path string) (string, error) {
		return files[path], nil
	}, func(path, content string) error {
		files[path] = content
		return nil
	}, func(path string) error {
		delete(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files["file.txt"] != "after\ncontext\n" || files["added.txt"] != "added" {
		t.Fatalf("unexpected files: %q", files)
	}
}
