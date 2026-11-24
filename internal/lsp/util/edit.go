package util

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/security"
)

func applyTextEdits(uri protocol.DocumentUri, edits []protocol.TextEdit) error {
	path, err := workspacePath(uri)
	if err != nil {
		return err
	}
	if len(edits) == 0 {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Resolve all ranges against the original document. Editing the raw bytes
	// preserves mixed line endings and permits intentional newline deletion.
	lineStarts := []int{0}
	for i := 0; i < len(content); i++ {
		if content[i] == '\r' {
			if i+1 < len(content) && content[i+1] == '\n' {
				i++
			}
			lineStarts = append(lineStarts, i+1)
		} else if content[i] == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	type byteEdit struct {
		start, end int
		text       string
	}
	sortedEdits := make([]byteEdit, 0, len(edits))
	for i, edit := range edits {
		if positionLess(edit.Range.End, edit.Range.Start) {
			return fmt.Errorf("edit %d has a reversed range", i)
		}
		start, err := positionOffset(content, lineStarts, edit.Range.Start)
		if err != nil {
			return fmt.Errorf("invalid start of edit %d: %w", i, err)
		}
		end, err := positionOffset(content, lineStarts, edit.Range.End)
		if err != nil {
			return fmt.Errorf("invalid end of edit %d: %w", i, err)
		}
		sortedEdits = append(sortedEdits, byteEdit{start, end, edit.NewText})
	}

	// Ranges are half-open. Insertions at the same position keep their array
	// order and precede a replacement starting there, as required by LSP.
	sort.SliceStable(sortedEdits, func(i, j int) bool {
		if sortedEdits[i].start != sortedEdits[j].start {
			return sortedEdits[i].start < sortedEdits[j].start
		}
		return sortedEdits[i].end < sortedEdits[j].end
	})
	var updated strings.Builder
	cursor := 0
	for _, edit := range sortedEdits {
		if edit.start < cursor {
			return fmt.Errorf("overlapping edits detected")
		}
		updated.Write(content[cursor:edit.start])
		updated.WriteString(edit.text)
		cursor = edit.end
	}
	updated.Write(content[cursor:])
	if err := os.WriteFile(path, []byte(updated.String()), 0o644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

// positionOffset translates LSP's default UTF-16 positions into UTF-8 byte
// offsets. Positions beyond a line are clamped to its end, excluding its EOL.
func positionOffset(content []byte, lineStarts []int, position protocol.Position) (int, error) {
	if uint64(position.Line) >= uint64(len(lineStarts)) {
		return len(content), nil
	}
	start := lineStarts[position.Line]
	end := len(content)
	if int(position.Line)+1 < len(lineStarts) {
		end = lineStarts[position.Line+1]
		if end > start && content[end-1] == '\n' {
			end--
		}
		if end > start && content[end-1] == '\r' {
			end--
		}
	}
	var units uint32
	for offset := start; offset < end; {
		if units == position.Character {
			return offset, nil
		}
		r, size := utf8.DecodeRune(content[offset:end])
		width := uint32(1)
		if r > 0xFFFF {
			width = 2
		}
		if position.Character-units < width {
			return 0, fmt.Errorf("character %d splits a UTF-16 surrogate pair", position.Character)
		}
		units += width
		offset += size
	}
	return end, nil
}

func positionLess(a, b protocol.Position) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
}

// applyDocumentChange applies a DocumentChange (create/rename/delete operations)
func applyDocumentChange(change protocol.DocumentChange) error {
	if !change.Valid() {
		return fmt.Errorf("invalid document change")
	}

	if change.CreateFile != nil {
		path, err := workspacePath(change.CreateFile.URI)
		if err != nil {
			return err
		}
		options := change.CreateFile.Options
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if options != nil && options.Overwrite {
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		}
		file, err := os.OpenFile(path, flags, 0o644)
		if err != nil {
			if os.IsExist(err) && options != nil && options.IgnoreIfExists && !options.Overwrite {
				return nil
			}
			return fmt.Errorf("failed to create file: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("failed to close created file: %w", err)
		}
	}

	if change.DeleteFile != nil {
		path, err := workspacePath(change.DeleteFile.URI)
		if err != nil {
			return err
		}
		options := change.DeleteFile.Options
		if options != nil && options.Recursive {
			// RemoveAll ignores missing paths, so explicitly enforce the LSP
			// default before performing the recursive operation.
			if _, err = os.Lstat(path); err == nil {
				err = os.RemoveAll(path)
			}
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			if os.IsNotExist(err) && options != nil && options.IgnoreIfNotExists {
				return nil
			}
			return fmt.Errorf("failed to delete file: %w", err)
		}
	}

	if change.RenameFile != nil {
		oldPath, err := workspacePath(change.RenameFile.OldURI)
		if err != nil {
			return err
		}
		newPath, err := workspacePath(change.RenameFile.NewURI)
		if err != nil {
			return err
		}
		options := change.RenameFile.Options
		if options == nil || !options.Overwrite {
			if _, err := os.Lstat(newPath); err == nil {
				if options != nil && options.IgnoreIfExists {
					return nil
				}
				return fmt.Errorf("target file already exists and overwrite is not allowed: %s", newPath)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("failed to inspect rename target: %w", err)
			}
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			return fmt.Errorf("failed to rename file: %w", err)
		}
	}

	if change.TextDocumentEdit != nil {
		textEdits := make([]protocol.TextEdit, len(change.TextDocumentEdit.Edits))
		for i, edit := range change.TextDocumentEdit.Edits {
			var err error
			textEdits[i], err = edit.AsTextEdit()
			if err != nil {
				return fmt.Errorf("invalid edit type: %w", err)
			}
		}
		return applyTextEdits(change.TextDocumentEdit.TextDocument.URI, textEdits)
	}

	return nil
}

// WorkspaceEditPaths validates and returns every filesystem target in an edit
// before the edit is applied. This lets callers perform one approval decision
// without allowing a later, unvalidated operation to bypass workspace safety.
func WorkspaceEditPaths(edit protocol.WorkspaceEdit) ([]string, error) {
	paths := make(map[string]struct{})
	add := func(uri protocol.DocumentUri) error {
		path, err := workspacePath(uri)
		if err != nil {
			return err
		}
		paths[path] = struct{}{}
		return nil
	}

	for uri := range edit.Changes {
		if err := add(uri); err != nil {
			return nil, err
		}
	}
	for _, change := range edit.DocumentChanges {
		if !change.Valid() {
			return nil, fmt.Errorf("invalid document change")
		}
		switch {
		case change.CreateFile != nil:
			if err := add(change.CreateFile.URI); err != nil {
				return nil, err
			}
		case change.DeleteFile != nil:
			if err := add(change.DeleteFile.URI); err != nil {
				return nil, err
			}
		case change.RenameFile != nil:
			if err := add(change.RenameFile.OldURI); err != nil {
				return nil, err
			}
			if err := add(change.RenameFile.NewURI); err != nil {
				return nil, err
			}
		case change.TextDocumentEdit != nil:
			if err := add(change.TextDocumentEdit.TextDocument.URI); err != nil {
				return nil, err
			}
			for _, edit := range change.TextDocumentEdit.Edits {
				if _, err := edit.AsTextEdit(); err != nil {
					return nil, fmt.Errorf("invalid edit type: %w", err)
				}
			}
		}
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func workspacePath(uri protocol.DocumentUri) (string, error) {
	parsed, err := url.Parse(string(uri))
	if err != nil || parsed.Scheme != "file" {
		return "", fmt.Errorf("LSP workspace edit requires a file URI")
	}
	path := parsed.Path
	if path == "" {
		path = strings.TrimPrefix(string(uri), "file://")
	}
	return security.ResolveWorkspacePath(config.WorkingDirectory(), path)
}

// ApplyWorkspaceEdit applies the given WorkspaceEdit to the filesystem
func ApplyWorkspaceEdit(edit protocol.WorkspaceEdit) error {
	if _, err := WorkspaceEditPaths(edit); err != nil {
		return err
	}

	// Handle Changes field
	for uri, textEdits := range edit.Changes {
		if err := applyTextEdits(uri, textEdits); err != nil {
			return fmt.Errorf("failed to apply text edits: %w", err)
		}
	}

	// Handle DocumentChanges field
	for _, change := range edit.DocumentChanges {
		if err := applyDocumentChange(change); err != nil {
			return fmt.Errorf("failed to apply document change: %w", err)
		}
	}

	return nil
}
