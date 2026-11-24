package util

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/lsp/protocol"
)

func TestWorkspaceEditPathsAreUniqueAndSorted(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	first := filepath.Join(root, "b.go")
	second := filepath.Join(root, "a.go")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	paths, err := WorkspaceEditPaths(protocol.WorkspaceEdit{
		Changes: map[protocol.DocumentUri][]protocol.TextEdit{
			protocol.DocumentUri("file://" + first):  nil,
			protocol.DocumentUri("file://" + second): nil,
		},
		DocumentChanges: []protocol.DocumentChange{{
			TextDocumentEdit: &protocol.TextDocumentEdit{
				TextDocument: protocol.OptionalVersionedTextDocumentIdentifier{
					TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: protocol.DocumentUri("file://" + first)},
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(canonicalRoot, "a.go"), filepath.Join(canonicalRoot, "b.go")}
	if len(paths) != len(want) || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths=%v, want %v", paths, want)
	}
}

func TestWorkspaceEditPathsRejectInvalidDocumentChange(t *testing.T) {
	if _, err := WorkspaceEditPaths(protocol.WorkspaceEdit{DocumentChanges: []protocol.DocumentChange{{}}}); err == nil {
		t.Fatal("expected invalid document change error")
	}
}

func editRange(startLine, startChar, endLine, endChar uint32, text string) protocol.TextEdit {
	return protocol.TextEdit{
		Range: protocol.Range{
			Start: protocol.Position{Line: startLine, Character: startChar},
			End:   protocol.Position{Line: endLine, Character: endChar},
		},
		NewText: text,
	}
}

func TestApplyTextEditsPreservesDocumentContent(t *testing.T) {
	tests := []struct {
		name, original, want string
		edits                []protocol.TextEdit
	}{
		{"UTF16 positions", "a😀éz\n", "a😀Ez\n", []protocol.TextEdit{editRange(0, 3, 0, 4, "E")}},
		{"empty line remains", "first\nsecond\n", "\nsecond\n", []protocol.TextEdit{editRange(0, 0, 0, 5, "")}},
		{"remove final newline", "first\n", "first", []protocol.TextEdit{editRange(0, 5, 1, 0, "")}},
		{"mixed line endings", "first\r\nsecond\nthird\r", "first\r\nSECOND\nthird\r", []protocol.TextEdit{editRange(1, 0, 1, 6, "SECOND")}},
		{"CRLF replacement", "first\r\nsecond\r\n", "FIRST\r\nSECOND\r\n", []protocol.TextEdit{editRange(0, 0, 1, 6, "FIRST\r\nSECOND")}},
		{"adjacent ranges", "abcd", "ABcd", []protocol.TextEdit{editRange(0, 0, 0, 1, "A"), editRange(0, 1, 0, 2, "B")}},
		{"inserts retain order", "ab", "aXYb", []protocol.TextEdit{editRange(0, 1, 0, 1, "X"), editRange(0, 1, 0, 1, "Y")}},
		{"insert before replacement", "ab", "aXYB", []protocol.TextEdit{editRange(0, 1, 0, 1, "X"), editRange(0, 1, 0, 1, "Y"), editRange(0, 1, 0, 2, "B")}},
		{"unsorted ranges", "abcd", "AbcD", []protocol.TextEdit{editRange(0, 3, 0, 4, "D"), editRange(0, 0, 0, 1, "A")}},
		{"clamp character to line end", "first\nsecond", "first!\nsecond", []protocol.TextEdit{editRange(0, 100, 0, 100, "!")}},
		{"insert at empty document", "", "hello", []protocol.TextEdit{editRange(0, 0, 0, 0, "hello")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			path := filepath.Join(root, "document.txt")
			if err := os.WriteFile(path, []byte(tt.original), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := applyTextEdits(protocol.DocumentUri("file://"+path), tt.edits); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyTextEditsRejectsInvalidRangesWithoutWriting(t *testing.T) {
	for name, edits := range map[string][]protocol.TextEdit{
		"reversed":        {editRange(0, 4, 0, 3, "wrong")},
		"surrogate split": {editRange(0, 2, 0, 3, "wrong")},
		"overlap":         {editRange(0, 0, 0, 4, "wrong"), editRange(0, 3, 0, 5, "wrong")},
		"interior insert": {editRange(0, 0, 0, 4, "wrong"), editRange(0, 3, 0, 3, "wrong")},
		"clamped overlap": {editRange(0, 0, 0, 100, "wrong"), editRange(0, 4, 0, 99, "wrong")},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			path := filepath.Join(root, "document.txt")
			original := "a😀bc\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := applyTextEdits(protocol.DocumentUri("file://"+path), edits); err == nil {
				t.Fatal("expected invalid edit to fail")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != original {
				t.Fatalf("invalid edit changed original content: %q, %v", got, err)
			}
		})
	}
}

func TestDocumentResourceOperationsRespectExistingFiles(t *testing.T) {
	for _, operation := range []string{"create", "rename"} {
		for _, mode := range []string{"default", "empty options", "ignore", "overwrite", "overwrite and ignore"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				t.Chdir(root)
				source := filepath.Join(root, "source.txt")
				target := filepath.Join(root, "target.txt")
				for path, content := range map[string]string{source: "source", target: "target"} {
					if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				uri := protocol.DocumentUri("file://" + target)
				overwrite := mode == "overwrite" || mode == "overwrite and ignore"
				ignore := mode == "ignore" || mode == "overwrite and ignore"
				var change protocol.DocumentChange
				if operation == "create" {
					change.CreateFile = &protocol.CreateFile{URI: uri}
					if mode != "default" {
						change.CreateFile.Options = &protocol.CreateFileOptions{Overwrite: overwrite, IgnoreIfExists: ignore}
					}
				} else {
					change.RenameFile = &protocol.RenameFile{OldURI: protocol.DocumentUri("file://" + source), NewURI: uri}
					if mode != "default" {
						change.RenameFile.Options = &protocol.RenameFileOptions{Overwrite: overwrite, IgnoreIfExists: ignore}
					}
				}
				err := applyDocumentChange(change)
				wantError := !overwrite && !ignore
				if (err != nil) != wantError {
					t.Errorf("error = %v, want error %v", err, wantError)
				}
				want := "target"
				if overwrite {
					want = ""
					if operation == "rename" {
						want = "source"
					}
				}
				got, err := os.ReadFile(target)
				if err != nil || string(got) != want {
					t.Errorf("target content = %q, %v; want %q", got, err, want)
				}
				if operation == "rename" && !overwrite {
					got, err := os.ReadFile(source)
					if err != nil || string(got) != "source" {
						t.Errorf("source changed without overwrite: %q, %v", got, err)
					}
				}
			})
		}
	}
}

func TestDeleteFileRespectsIgnoreIfNotExists(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		for _, ignore := range []bool{false, true} {
			root := t.TempDir()
			t.Chdir(root)
			err := applyDocumentChange(protocol.DocumentChange{DeleteFile: &protocol.DeleteFile{
				URI:     protocol.DocumentUri("file://" + filepath.Join(root, "missing")),
				Options: &protocol.DeleteFileOptions{Recursive: recursive, IgnoreIfNotExists: ignore},
			}})
			if (err == nil) != ignore {
				t.Errorf("recursive=%v, ignore=%v: error = %v", recursive, ignore, err)
			}
		}
	}
}
