package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/permission"
)

type patchPermissionRecorder struct {
	permission.Service
	requests []permission.CreatePermissionRequest
	allow    bool
}

func (p *patchPermissionRecorder) RequestContext(_ context.Context, request permission.CreatePermissionRequest) bool {
	p.requests = append(p.requests, request)
	return p.allow
}

type patchHistoryVersion struct {
	path    string
	content string
}

type patchHistoryRecorder struct {
	history.Service
	versions []patchHistoryVersion
}

func (h *patchHistoryRecorder) GetByPathAndSession(context.Context, string, string) (history.File, error) {
	return history.File{}, errors.New("history entry not found")
}

func (h *patchHistoryRecorder) Create(_ context.Context, _, path, content string) (history.File, error) {
	return history.File{Path: path, Content: content}, nil
}

func (h *patchHistoryRecorder) CreateVersion(_ context.Context, _, path, content string) (history.File, error) {
	h.versions = append(h.versions, patchHistoryVersion{path: path, content: content})
	return history.File{Path: path, Content: content}, nil
}

func TestPatchMoveAuthorizesAndRecordsBothPaths(t *testing.T) {
	workspace, _ := boundaryWorkspace(t)
	source := filepath.Join(workspace, "source.txt")
	destination := filepath.Join(workspace, "destination.txt")
	if err := os.WriteFile(source, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := resolveWorkspaceToolPath(source)
	if err != nil {
		t.Fatal(err)
	}
	canonicalDestination, err := resolveWorkspaceToolPath(destination)
	if err != nil {
		t.Fatal(err)
	}
	recordFileRead(editTestContext(), canonicalSource)

	permissions := &patchPermissionRecorder{allow: true}
	historyRecorder := &patchHistoryRecorder{}
	tool := NewPatchTool(nil, permissions, historyRecorder)
	patchText := "*** Begin Patch\n*** Update File: " + source + "\n*** Move to: " + destination + "\n@@\n-old\n+new\n*** End Patch"
	response, err := tool.Run(editTestContext(), boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchText}))
	if err != nil || response.IsError {
		t.Fatalf("move patch failed: response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "new" {
		t.Fatalf("move destination content = %q, err=%v", content, err)
	}
	if len(permissions.requests) != 2 {
		t.Fatalf("move permission requests = %#v", permissions.requests)
	}
	if permissions.requests[0].Action != "delete" || permissions.requests[1].Action != "create" {
		t.Fatalf("move permission actions = %q, %q", permissions.requests[0].Action, permissions.requests[1].Action)
	}
	if permissions.requests[0].ScopeKind != permission.ScopeDirectory || permissions.requests[1].ScopeKind != permission.ScopeDirectory {
		t.Fatalf("move permission scope kinds = %q, %q", permissions.requests[0].ScopeKind, permissions.requests[1].ScopeKind)
	}
	sourcePermission := permissions.requests[0].Params.(EditPermissionsParams)
	destinationPermission := permissions.requests[1].Params.(EditPermissionsParams)
	if sourcePermission.FilePath != canonicalSource || destinationPermission.FilePath != canonicalDestination {
		t.Fatalf("move permission paths = %q, %q", sourcePermission.FilePath, destinationPermission.FilePath)
	}

	var metadata PatchResponseMetadata
	if err := json.Unmarshal([]byte(response.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.FilesChanged) != 2 || metadata.FilesChanged[0] != canonicalSource || metadata.FilesChanged[1] != canonicalDestination {
		t.Fatalf("move provenance paths = %#v", metadata.FilesChanged)
	}
	foundSourceDeletion := false
	foundDestinationCreation := false
	for _, version := range historyRecorder.versions {
		foundSourceDeletion = foundSourceDeletion || version.path == canonicalSource && version.content == ""
		foundDestinationCreation = foundDestinationCreation || version.path == canonicalDestination && version.content == "new"
	}
	if !foundSourceDeletion || !foundDestinationCreation {
		t.Fatalf("move history versions = %#v", historyRecorder.versions)
	}
}

func TestPatchMoveRejectsExistingDestinationBeforePermission(t *testing.T) {
	workspace, _ := boundaryWorkspace(t)
	source := filepath.Join(workspace, "source.txt")
	destination := filepath.Join(workspace, "destination.txt")
	if err := os.WriteFile(source, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalSource, err := resolveWorkspaceToolPath(source)
	if err != nil {
		t.Fatal(err)
	}
	recordFileRead(editTestContext(), canonicalSource)

	permissions := &patchPermissionRecorder{allow: true}
	tool := NewPatchTool(nil, permissions, &patchHistoryRecorder{})
	patchText := "*** Begin Patch\n*** Update File: " + source + "\n*** Move to: " + destination + "\n@@\n-old\n+new\n*** End Patch"
	response, err := tool.Run(editTestContext(), boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchText}))
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError {
		t.Fatalf("existing move destination was overwritten: %+v", response)
	}
	if len(permissions.requests) != 0 {
		t.Fatalf("permission was requested for an invalid move: %#v", permissions.requests)
	}
	sourceContent, sourceErr := os.ReadFile(source)
	destinationContent, destinationErr := os.ReadFile(destination)
	if sourceErr != nil || destinationErr != nil || string(sourceContent) != "old" || string(destinationContent) != "existing" {
		t.Fatalf("files changed after rejected move: source=%q/%v destination=%q/%v", sourceContent, sourceErr, destinationContent, destinationErr)
	}
}
