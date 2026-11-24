package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadTextFilePagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.txt")
	if err := os.WriteFile(path, []byte("first\n\nthird\nfourth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		offset  int
		limit   int
		content string
	}{
		{name: "first page", limit: 3, content: "first\n\nthird"},
		{name: "blank line", offset: 1, limit: 1, content: ""},
		{name: "last page", offset: 3, limit: 1, content: "fourth"},
		{name: "past end", offset: 20, limit: 2, content: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, count, err := readTextFile(path, tc.offset, tc.limit)
			if err != nil || count != 4 || content != tc.content {
				t.Fatalf("readTextFile() = %q, %d, %v; want %q, 4, nil", content, count, err, tc.content)
			}
		})
	}
}

func TestViewReportsRemainingLine(t *testing.T) {
	workspace, _ := boundaryWorkspace(t)
	path := filepath.Join(workspace, "lines.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	response, err := NewViewTool(nil).Run(context.Background(), boundaryToolCall(t, ViewToolName, ViewParams{FilePath: path, Limit: 1}))
	if err != nil || response.IsError || !strings.Contains(response.Content, "File has more lines") {
		t.Fatalf("response = %+v, err = %v; want remaining-lines notice", response, err)
	}
}

func TestViewEmptyAndInvalidPages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   string
		offset    int
		wantLine  bool
		wantError bool
	}{
		{name: "blank line", content: "\n", wantLine: true},
		{name: "empty file"},
		{name: "past end", content: "first\n", offset: 10},
		{name: "negative offset", content: "first\n", offset: -1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, _ := boundaryWorkspace(t)
			path := filepath.Join(workspace, "lines.txt")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			response, err := NewViewTool(nil).Run(context.Background(), boundaryToolCall(t, ViewToolName, ViewParams{FilePath: path, Offset: tc.offset, Limit: 1}))
			if err != nil || response.IsError != tc.wantError || strings.Contains(response.Content, "|") != tc.wantLine {
				t.Fatalf("response = %+v, err = %v; want line = %v and error = %v", response, err, tc.wantLine, tc.wantError)
			}
		})
	}
}

func TestReadTextFileLongLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want string
	}{
		{name: "maximum file size", line: strings.Repeat("a", MaxReadSize), want: strings.Repeat("a", MaxLineLength) + "..."},
		{name: "unicode", line: strings.Repeat("界", MaxLineLength+1), want: strings.Repeat("界", MaxLineLength) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "long.txt")
			if err := os.WriteFile(path, []byte(tc.line), 0o644); err != nil {
				t.Fatal(err)
			}
			content, count, err := readTextFile(path, 0, 1)
			if err != nil || count != 1 || content != tc.want || !utf8.ValidString(content) {
				t.Fatalf("readTextFile() returned %d bytes, %d lines, %v; want %d bytes of valid UTF-8 and 1 line", len(content), count, err, len(tc.want))
			}
		})
	}
}
