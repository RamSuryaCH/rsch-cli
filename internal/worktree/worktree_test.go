package worktree

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestParseUntrackedPreservesNamesAndSkipsRenameSources(t *testing.T) {
	output := []byte("R  renamed.txt\x00?? tracked-source.txt\x00??  leading.txt \x00?? line\nbreak.txt\x00?? \t\x00")
	want := []string{" leading.txt ", "line\nbreak.txt", "\t"}
	if got := parseUntracked(output); !reflect.DeepEqual(got, want) {
		t.Fatalf("untracked paths = %q, want %q", got, want)
	}
}

func TestCreateForkPreservesDirtyParentState(t *testing.T) {
	if !process.Exists("git") {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	gitTestCommand(t, root, "init", "-q")
	gitTestCommand(t, root, "config", "user.email", "rsch-test@example.invalid")
	gitTestCommand(t, root, "config", "user.name", "RSCH Test")
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "add", "tracked.txt")
	gitTestCommand(t, root, "commit", "-qm", "base")
	if err := os.WriteFile(tracked, []byte("parent change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(root, "new.txt")
	if err := os.WriteFile(untracked, []byte("new parent file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spacedName := " leading and trailing.txt "
	if runtime.GOOS == "windows" {
		spacedName = " leading.txt"
	}
	if err := os.WriteFile(filepath.Join(root, spacedName), []byte("literal filename\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fork, err := NewManager(root).CreateFork(context.Background(), root, "dirty-parent")
	if err != nil {
		t.Fatal(err)
	}
	parentData, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	childData, err := os.ReadFile(filepath.Join(fork, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(parentData) != "parent change\n" || string(childData) != "parent change\n" {
		t.Fatalf("parent=%q child=%q", parentData, childData)
	}
	childUntracked, err := os.ReadFile(filepath.Join(fork, "new.txt"))
	if err != nil || string(childUntracked) != "new parent file\n" {
		t.Fatalf("child untracked=%q err=%v", childUntracked, err)
	}
	childSpaced, err := os.ReadFile(filepath.Join(fork, spacedName))
	if err != nil || string(childSpaced) != "literal filename\n" {
		t.Fatalf("child spaced file=%q err=%v", childSpaced, err)
	}
}

func gitTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	result, err := process.RunOutput(context.Background(), process.Spec{Argv: append([]string{"git"}, args...), Directory: root})
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, result.Output)
	}
}
