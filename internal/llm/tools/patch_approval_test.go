package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchRejectsApprovalTimeChanges(t *testing.T) {
	for _, operation := range []string{"update", "delete", "add", "move"} {
		t.Run(operation, func(t *testing.T) {
			workspace, _ := boundaryWorkspace(t)
			source := filepath.Join(workspace, "source.txt")
			target := filepath.Join(workspace, "target.txt")
			unchanged := filepath.Join(workspace, "a-unchanged.txt")
			for _, path := range []string{source, unchanged} {
				if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				canonical, err := resolveWorkspaceToolPath(path)
				if err != nil {
					t.Fatal(err)
				}
				recordFileRead(editTestContext(), canonical)
			}
			mutatedPath := source
			patchText := "*** Begin Patch\n*** Update File: " + unchanged + "\n@@\n-old\n+new\n"
			switch operation {
			case "update":
				patchText += "*** Update File: " + source + "\n@@\n-old\n+new\n"
			case "delete":
				patchText += "*** Delete File: " + source + "\n"
			case "add":
				patchText += "*** Add File: " + target + "\n+new\n"
				mutatedPath = target
			case "move":
				patchText += "*** Update File: " + source + "\n*** Move to: " + target + "\n@@\n-old\n+new\n"
				mutatedPath = target
			}
			patchText += "*** End Patch"
			permissions := permissionHook{allow: true, hook: func() {
				if err := os.WriteFile(mutatedPath, []byte("external"), 0o644); err != nil {
					t.Fatal(err)
				}
			}}
			history := &patchHistoryRecorder{}
			response, err := NewPatchTool(nil, permissions, history).Run(editTestContext(), boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchText}))
			if err != nil || !response.IsError || !strings.Contains(response.Content, "after approval") {
				t.Fatalf("response = %+v, err = %v; want approval-time change error", response, err)
			}
			for path, expected := range map[string]string{mutatedPath: "external", unchanged: "old"} {
				content, err := os.ReadFile(path)
				if err != nil || string(content) != expected {
					t.Fatalf("%s contains %q, err = %v; want %q", path, content, err, expected)
				}
			}
			if len(history.versions) != 0 {
				t.Fatalf("rejected patch recorded history: %#v", history.versions)
			}
		})
	}
}

func TestPatchCanceledDuringApprovalDoesNotWrite(t *testing.T) {
	workspace, _ := boundaryWorkspace(t)
	path := filepath.Join(workspace, "new.txt")
	ctx, cancel := context.WithCancel(editTestContext())
	defer cancel()
	permissions := permissionHook{allow: true, hook: cancel}
	patchText := "*** Begin Patch\n*** Add File: " + path + "\n+new\n*** End Patch"
	response, err := NewPatchTool(nil, permissions, &patchHistoryRecorder{}).Run(ctx, boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchText}))
	if !errors.Is(err, context.Canceled) || !response.IsError {
		t.Fatalf("response = %+v, err = %v; want cancellation error", response, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("canceled patch created a file: %v", err)
	}
}

func TestPatchRejectsApprovalTimeSymlinkChange(t *testing.T) {
	workspace, _ := boundaryWorkspace(t)
	source := filepath.Join(workspace, "source.txt")
	target := filepath.Join(workspace, "target.txt")
	for _, path := range []string{source, target} {
		if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	canonicalSource, err := resolveWorkspaceToolPath(source)
	if err != nil {
		t.Fatal(err)
	}
	recordFileRead(editTestContext(), canonicalSource)
	permissions := permissionHook{allow: true, hook: func() {
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, source); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}}
	patchText := "*** Begin Patch\n*** Update File: " + source + "\n@@\n-old\n+new\n*** End Patch"
	response, err := NewPatchTool(nil, permissions, &patchHistoryRecorder{}).Run(editTestContext(), boundaryToolCall(t, PatchToolName, PatchParams{PatchText: patchText}))
	if err != nil || !response.IsError || !strings.Contains(response.Content, "path changed after approval") {
		t.Fatalf("response = %+v, err = %v; want approval-time path change error", response, err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "old" {
		t.Fatalf("symlink target contains %q, err = %v; want original content", content, err)
	}
}
