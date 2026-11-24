package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/permission"
)

type permissionHook struct {
	permission.Service
	hook  func()
	allow bool
}

func (p permissionHook) Request(permission.CreatePermissionRequest) bool {
	if p.hook != nil {
		p.hook()
	}
	return p.allow
}

func (p permissionHook) RequestContext(context.Context, permission.CreatePermissionRequest) bool {
	if p.hook != nil {
		p.hook()
	}
	return p.allow
}

func TestWriteAllowsExplicitEmptyContent(t *testing.T) {
	path := editTestPath(t, "write-empty-regression.txt")
	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession("session-1")
	tool := NewWriteTool(nil, permissions, &editHistoryStub{})

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `","content":""}`})
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError {
		t.Fatalf("write response = %+v, want success", response)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("content = %q, want empty", content)
	}
	var metadata WriteResponseMetadata
	if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ContentHash != contentSHA256(nil) {
		t.Fatalf("content hash = %q, want %q", metadata.ContentHash, contentSHA256(nil))
	}
}

func TestWriteRejectsMissingContent(t *testing.T) {
	path := editTestPath(t, "write-missing-content.txt")
	tool := NewWriteTool(nil, permission.NewPermissionService(), &editHistoryStub{})

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError || !strings.Contains(response.Content, "content is required") {
		t.Fatalf("response = %+v, want missing-content error", response)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("missing-content call mutated path: %v", statErr)
	}
}

func TestWriteDeniedDoesNotCreateParentDirectory(t *testing.T) {
	parent := editTestPath(t, "write-denied-parent")
	path := filepath.Join(parent, "child", "file.txt")
	ctx, cancel := context.WithCancel(editTestContext())
	cancel()
	tool := NewWriteTool(nil, permission.NewPermissionService(), &editHistoryStub{})

	response, err := tool.Run(ctx, ToolCall{Input: `{"file_path":"` + path + `","content":"blocked"}`})
	if !errors.Is(err, permission.ErrorPermissionDenied) {
		t.Fatalf("error = %v, want permission denied", err)
	}
	if !response.IsError {
		t.Fatalf("response = %+v, want IsError", response)
	}
	if _, statErr := os.Stat(parent); !os.IsNotExist(statErr) {
		t.Fatalf("denied write created parent directory: %v", statErr)
	}
}

func TestWriteRejectsApprovalTimeContentChange(t *testing.T) {
	path := editTestPath(t, "write-approval-race.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
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
	tool := NewWriteTool(nil, permissions, &editHistoryStub{content: "original"})

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `","content":"replacement"}`})
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

func TestWriteReportsCommittedMutationWhenHistoryFails(t *testing.T) {
	path := editTestPath(t, "write-history-warning.txt")
	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession("session-1")
	files := &editHistoryStub{
		getErr:    errors.New("history unavailable"),
		createErr: errors.New("history unavailable"),
	}
	tool := NewWriteTool(nil, permissions, files)

	response, err := tool.Run(editTestContext(), ToolCall{Input: `{"file_path":"` + path + `","content":"committed"}`})
	if err != nil {
		t.Fatal(err)
	}
	if response.IsError || !strings.Contains(response.Content, "file was written, but history initialization failed") {
		t.Fatalf("response = %+v, want committed success with history warning", response)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "committed" {
		t.Fatalf("content = %q, want committed", content)
	}
}
