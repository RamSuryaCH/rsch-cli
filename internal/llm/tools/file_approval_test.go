package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileToolsRejectApprovalTimeSymlinkChanges(t *testing.T) {
	for _, operation := range []string{"write-existing", "edit-replace", "edit-delete", "write-new", "edit-new"} {
		t.Run(operation, func(t *testing.T) {
			workspace, outside := boundaryWorkspace(t)
			parent := filepath.Join(workspace, "files")
			if err := os.Mkdir(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "file.txt")
			outsidePath := filepath.Join(outside, "file.txt")
			creating := strings.HasSuffix(operation, "-new")
			if !creating {
				for _, existingPath := range []string{path, outsidePath} {
					if err := os.WriteFile(existingPath, []byte("old"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				canonicalPath, err := resolveWorkspaceToolPath(path)
				if err != nil {
					t.Fatal(err)
				}
				recordFileRead(editTestContext(), canonicalPath)
			}
			permissions := permissionHook{allow: true, hook: func() {
				link, target := path, outsidePath
				if creating {
					link, target = parent, outside
				}
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}}
			files := &editHistoryStub{}
			var tool BaseTool
			var params any
			if strings.HasPrefix(operation, "write-") {
				tool = NewWriteTool(nil, permissions, files)
				params = WriteParams{FilePath: path, Content: "new"}
			} else {
				tool = NewEditTool(nil, permissions, files)
				edit := EditParams{FilePath: path, OldString: "old", NewString: "new"}
				if creating {
					edit.OldString = ""
				} else if operation == "edit-delete" {
					edit.NewString = ""
				}
				params = edit
			}
			response, err := tool.Run(editTestContext(), boundaryToolCall(t, tool.Info().Name, params))
			if err != nil || !response.IsError || !strings.Contains(response.Content, "path changed after approval") {
				t.Fatalf("response = %+v, err = %v; want approval-time path change error", response, err)
			}
			if creating {
				if _, err := os.Stat(outsidePath); !os.IsNotExist(err) {
					t.Fatalf("tool created an outside-workspace file: %v", err)
				}
			} else {
				content, err := os.ReadFile(outsidePath)
				if err != nil || string(content) != "old" {
					t.Fatalf("outside file contains %q, err = %v; want original content", content, err)
				}
			}
			if files.createCalls != 0 || files.versionCalls != 0 {
				t.Fatalf("rejected operation recorded history: %+v", files)
			}
		})
	}
}
