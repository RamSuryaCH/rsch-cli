package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

func TestFileWatchRegistrationsAreClientScoped(t *testing.T) {
	first := &Client{}
	second := &Client{}
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	first.RegisterFileWatchHandler(func(id string, watchers []protocol.FileSystemWatcher) {
		if id != "watch-go" || len(watchers) != 1 {
			t.Errorf("unexpected first registration: id=%q watchers=%d", id, len(watchers))
		}
		firstCalls.Add(1)
	})
	second.RegisterFileWatchHandler(func(string, []protocol.FileSystemWatcher) {
		secondCalls.Add(1)
	})

	params := json.RawMessage(`{"registrations":[{"id":"watch-go","method":"workspace/didChangeWatchedFiles","registerOptions":{"watchers":[{"globPattern":"**/*.go"}]}}]}`)
	if _, err := first.HandleRegisterCapability(params); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("registration leaked across clients: first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
}

func TestHandleApplyEditRequiresRegisteredApproval(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	approved := false
	SetWorkspaceEditApproval(func(paths []string) bool {
		approved = len(paths) == 1
		return false
	})
	defer SetWorkspaceEditApproval(nil)

	params, err := json.Marshal(protocol.ApplyWorkspaceEditParams{Edit: protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentUri][]protocol.TextEdit{
			protocol.DocumentUri("file://" + path): {{NewText: "package new\n"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := HandleApplyEdit(params)
	if err != nil {
		t.Fatal(err)
	}
	response := result.(protocol.ApplyWorkspaceEditResult)
	if response.Applied || !approved {
		t.Fatalf("unexpected apply result: %+v approved=%t", response, approved)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "package old\n" {
		t.Fatalf("denied workspace edit changed file: %q", content)
	}
}

func TestHandleApplyEditDeniesWhenApprovalIsNotConfigured(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	SetWorkspaceEditApproval(nil)
	params, err := json.Marshal(protocol.ApplyWorkspaceEditParams{Edit: protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentUri][]protocol.TextEdit{
			protocol.DocumentUri("file://" + path): {{NewText: "package new\n"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := HandleApplyEdit(params)
	if err != nil {
		t.Fatal(err)
	}
	response := result.(protocol.ApplyWorkspaceEditResult)
	if response.Applied {
		t.Fatalf("workspace edit applied without an approval handler: %+v", response)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "package old\n" {
		t.Fatalf("unapproved workspace edit changed file: %q", content)
	}
}

func TestHandleApplyEditRejectsWorkspaceEscapeBeforeApproval(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	called := false
	SetWorkspaceEditApproval(func([]string) bool {
		called = true
		return true
	})
	defer SetWorkspaceEditApproval(nil)

	params, err := json.Marshal(protocol.ApplyWorkspaceEditParams{Edit: protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentUri][]protocol.TextEdit{
			protocol.DocumentUri("file://" + filepath.Join(root, "..", "outside.go")): {{NewText: "unsafe"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := HandleApplyEdit(params)
	if err != nil {
		t.Fatal(err)
	}
	response := result.(protocol.ApplyWorkspaceEditResult)
	if response.Applied || called {
		t.Fatalf("workspace escape was not rejected before approval: %+v called=%t", response, called)
	}
}
