package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/permission"
)

type boundaryHistoryStub struct {
	history.Service
}

func (s *boundaryHistoryStub) Create(context.Context, string, string, string) (history.File, error) {
	return history.File{}, nil
}

func (s *boundaryHistoryStub) CreateVersion(context.Context, string, string, string) (history.File, error) {
	return history.File{}, nil
}

func (s *boundaryHistoryStub) GetByPathAndSession(context.Context, string, string) (history.File, error) {
	return history.File{}, errors.New("history entry not found")
}

func boundaryToolCall(t *testing.T, name string, params any) ToolCall {
	t.Helper()
	input, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return ToolCall{Name: name, Input: string(input)}
}

func boundaryWorkspace(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDirectory) })
	return workspace, outside
}

func assertOutsideWorkspace(t *testing.T, response ToolResponse) {
	t.Helper()
	if !response.IsError {
		t.Fatalf("response unexpectedly succeeded: %+v", response)
	}
	if !strings.Contains(response.Content, "outside the workspace") {
		t.Fatalf("response = %q, want workspace-boundary error", response.Content)
	}
}

func TestViewToolWorkspaceBoundary(t *testing.T) {
	workspace, outside := boundaryWorkspace(t)
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "valid.txt"), []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewViewTool(nil)
	for _, path := range []string{outsideFile, filepath.Join("..", "outside", "secret.txt")} {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, ViewToolName, ViewParams{FilePath: path}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	}

	symlinkPath := filepath.Join(workspace, "escape.txt")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, ViewToolName, ViewParams{FilePath: "escape.txt"}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	} else {
		t.Logf("symlink escape case skipped: %v", err)
	}

	response, err := tool.Run(context.Background(), boundaryToolCall(t, ViewToolName, ViewParams{FilePath: "valid.txt"}))
	if err != nil || response.IsError || !strings.Contains(response.Content, "valid") {
		t.Fatalf("valid workspace view = %+v, err = %v", response, err)
	}
}

func TestGrepToolWorkspaceBoundary(t *testing.T) {
	workspace, outside := boundaryWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "valid.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(outside, "search")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	for _, path := range []string{outsideDir, filepath.Join("..", "outside", "search")} {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, GrepToolName, GrepParams{Pattern: "needle", Path: path}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	}

	symlinkPath := filepath.Join(workspace, "escape")
	if err := os.Symlink(outsideDir, symlinkPath); err == nil {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, GrepToolName, GrepParams{Pattern: "needle", Path: "escape"}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	} else {
		t.Logf("symlink escape case skipped: %v", err)
	}

	response, err := tool.Run(context.Background(), boundaryToolCall(t, GrepToolName, GrepParams{Pattern: "needle", Path: "."}))
	if err != nil || response.IsError || !strings.Contains(response.Content, "valid.txt") {
		t.Fatalf("valid workspace grep = %+v, err = %v", response, err)
	}
}

func TestGlobToolWorkspaceBoundary(t *testing.T) {
	workspace, outside := boundaryWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "valid.go"), []byte("package valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGlobTool()
	for _, params := range []GlobParams{
		{Pattern: "../outside/*.go"},
		{Pattern: "*.go", Path: filepath.Join("..", "outside")},
	} {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, GlobToolName, params))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	}

	symlinkPath := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, symlinkPath); err == nil {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, GlobToolName, GlobParams{Pattern: "*.go", Path: "escape"}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	} else {
		t.Logf("symlink escape case skipped: %v", err)
	}

	response, err := tool.Run(context.Background(), boundaryToolCall(t, GlobToolName, GlobParams{Pattern: "*.go"}))
	if err != nil || response.IsError || !strings.Contains(response.Content, "valid.go") {
		t.Fatalf("valid workspace glob = %+v, err = %v", response, err)
	}
}

func TestLsToolWorkspaceBoundary(t *testing.T) {
	workspace, outside := boundaryWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspace, "valid.txt"), []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewLsTool()
	for _, path := range []string{outside, filepath.Join("..", "outside")} {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, LSToolName, LSParams{Path: path}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	}

	symlinkPath := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, symlinkPath); err == nil {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, LSToolName, LSParams{Path: "escape"}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	} else {
		t.Logf("symlink escape case skipped: %v", err)
	}

	response, err := tool.Run(context.Background(), boundaryToolCall(t, LSToolName, LSParams{Path: "."}))
	if err != nil || response.IsError || !strings.Contains(response.Content, "valid.txt") {
		t.Fatalf("valid workspace listing = %+v, err = %v", response, err)
	}
}

func TestPatchToolWorkspaceBoundary(t *testing.T) {
	workspace, outside := boundaryWorkspace(t)
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	validFile := filepath.Join(workspace, "valid.txt")
	if err := os.WriteFile(validFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewPatchTool(nil, permission.NewPermissionService(), &boundaryHistoryStub{})
	patchFor := func(path string) string {
		return "*** Begin Patch\n*** Update File: " + path + "\n@@\n-old\n+new\n*** End Patch"
	}
	for _, path := range []string{outsideFile, filepath.Join("..", "outside", "secret.txt")} {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchFor(path)}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	}

	symlinkPath := filepath.Join(workspace, "escape.txt")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		response, err := tool.Run(context.Background(), boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchFor("escape.txt")}))
		if err != nil {
			t.Fatal(err)
		}
		assertOutsideWorkspace(t, response)
	} else {
		t.Logf("symlink escape case skipped: %v", err)
	}

	resolvedValidFile, err := resolveWorkspaceToolPath(validFile)
	if err != nil {
		t.Fatal(err)
	}
	patchContext := context.WithValue(context.Background(), SessionIDContextKey, "session-1")
	patchContext = context.WithValue(patchContext, MessageIDContextKey, "message-1")
	recordFileRead(patchContext, resolvedValidFile)
	permissions := permission.NewPermissionService()
	permissions.AutoApproveSession("session-1")
	tool = NewPatchTool(nil, permissions, &boundaryHistoryStub{})
	response, err := tool.Run(patchContext, boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchFor("valid.txt")}))
	if err != nil || response.IsError {
		t.Fatalf("valid workspace patch = %+v, err = %v", response, err)
	}
	content, err := os.ReadFile(validFile)
	if err != nil || string(content) != "new" {
		t.Fatalf("valid workspace patch content = %q, err = %v", content, err)
	}
}

func TestDiagnosticsToolRejectsOutsideWorkspacePathBeforeLSPAccess(t *testing.T) {
	_, outside := boundaryWorkspace(t)
	response, err := NewDiagnosticsTool(nil).Run(context.Background(), boundaryToolCall(t, DiagnosticsToolName, DiagnosticsParams{FilePath: filepath.Join(outside, "secret.go")}))
	if err != nil {
		t.Fatal(err)
	}
	assertOutsideWorkspace(t, response)
}
