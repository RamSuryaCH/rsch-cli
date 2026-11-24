package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/permission"
)

type editHistoryStub struct {
	history.Service
	content      string
	createCalls  int
	versionCalls int
	getErr       error
	createErr    error
	versionErr   error
}

func (s *editHistoryStub) Create(context.Context, string, string, string) (history.File, error) {
	s.createCalls++
	return history.File{}, s.createErr
}

func (s *editHistoryStub) CreateVersion(context.Context, string, string, string) (history.File, error) {
	s.versionCalls++
	return history.File{}, s.versionErr
}

func (s *editHistoryStub) GetByPathAndSession(context.Context, string, string) (history.File, error) {
	return history.File{Content: s.content}, s.getErr
}

func editTestContext() context.Context {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "session-1")
	return context.WithValue(ctx, MessageIDContextKey, "message-1")
}

func editTestPath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(config.WorkingDirectory(), name)
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func TestEditRunCreateReturnsCreateResult(t *testing.T) {
	path := editTestPath(t, "edit-create-regression.txt")
	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession("session-1")
	files := &editHistoryStub{}
	tool := NewEditTool(nil, permissions, files)

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `","new_string":"created"}`})
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError || !strings.Contains(response.Content, "File created:") {
		t.Fatalf("create response = %+v, want a successful create result", response)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "created" {
		t.Fatalf("created content = %q, want %q", content, "created")
	}
	if files.createCalls != 1 || files.versionCalls != 1 {
		t.Fatalf("history calls = (%d, %d), want one create and one version", files.createCalls, files.versionCalls)
	}
}

func TestEditRunDeleteReturnsDeleteResult(t *testing.T) {
	path := editTestPath(t, "edit-delete-regression.txt")
	if err := os.WriteFile(path, []byte("before remove after"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordFileRead(editTestContext(), path)

	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession("session-1")
	files := &editHistoryStub{content: "before remove after"}
	tool := NewEditTool(nil, permissions, files)

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `","old_string":" remove"}`})
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError || !strings.Contains(response.Content, "Content deleted from file:") {
		t.Fatalf("delete response = %+v, want a successful delete result", response)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before after" {
		t.Fatalf("remaining content = %q, want %q", content, "before after")
	}
}

func TestEditDeniedCreateDoesNotCreateParentDirectory(t *testing.T) {
	parent := editTestPath(t, "edit-denied-parent")
	path := filepath.Join(parent, "child", "file.txt")
	ctx, cancel := context.WithCancel(editTestContext())
	cancel()
	tool := NewEditTool(nil, permission.NewPermissionService(), &editHistoryStub{})

	response, err := tool.Run(ctx, ToolCall{Input: `{"file_path":"` + path + `","new_string":"blocked"}`})
	if !errors.Is(err, permission.ErrorPermissionDenied) {
		t.Fatalf("error = %v, want permission denied", err)
	}
	if !response.IsError {
		t.Fatalf("response = %+v, want IsError", response)
	}
	if _, statErr := os.Stat(parent); !os.IsNotExist(statErr) {
		t.Fatalf("denied edit created parent directory: %v", statErr)
	}
}

func TestEditRejectsApprovalTimeContentChange(t *testing.T) {
	path := editTestPath(t, "edit-approval-race.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordFileRead(editTestContext(), path)
	permissions := permissionHook{
		Service: permission.NewPermissionService(),
		allow:   true,
		hook: func() {
			if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
				t.Errorf("approval hook: %v", err)
			}
		},
	}
	tool := NewEditTool(nil, permissions, &editHistoryStub{content: "before"})

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `","old_string":"before","new_string":"after"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError || !strings.Contains(response.Content, "changed after approval") {
		t.Fatalf("response = %+v, want approval-time freshness error", response)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "external" {
		t.Fatalf("content = %q, want external mutation preserved", content)
	}
}
