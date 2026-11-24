package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestSearchFilesWithRegexKeepsNewestMatchesBounded(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	for index := 0; index < 6; index++ {
		path := filepath.Join(root, fmt.Sprintf("%d.txt", index))
		if err := os.WriteFile(path, []byte("needle\nneedle again\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(index) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	matches, truncated, err := searchFilesWithRegex(context.Background(), "needle", root, "*.txt", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(matches) != 2 {
		t.Fatalf("matches=%d truncated=%t, want 2/true", len(matches), truncated)
	}
	if filepath.Base(matches[0].path) != "5.txt" || filepath.Base(matches[1].path) != "4.txt" {
		t.Fatalf("unexpected newest matches: %s, %s", matches[0].path, matches[1].path)
	}
	if matches[0].lineNum != 1 || matches[0].lineText != "needle" {
		t.Fatalf("fallback should return one first-line match per file: %+v", matches[0])
	}
}

func TestSearchFilesWithRegexHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := searchFilesWithRegex(ctx, "needle", t.TempDir(), "", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestSearchWithRipgrepIsBoundedAndNewestFirst(t *testing.T) {
	if !process.Exists("rg") {
		t.Skip("ripgrep is unavailable")
	}
	root := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	line := "needle " + strings.Repeat("x", maxGrepLineBytes*2)
	for index := 0; index < 120; index++ {
		path := filepath.Join(root, fmt.Sprintf("%03d.txt", index))
		if err := os.WriteFile(path, []byte(line+"\nneedle second line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(index) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	matches, truncated, err := searchWithRipgrep(context.Background(), "needle", root, "*.txt", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(matches) != 10 {
		t.Fatalf("matches=%d truncated=%t, want 10/true", len(matches), truncated)
	}
	if filepath.Base(matches[0].path) != "119.txt" || filepath.Base(matches[9].path) != "110.txt" {
		t.Fatalf("unexpected newest range: first=%s last=%s", matches[0].path, matches[9].path)
	}
	if len(matches[0].lineText) > maxGrepLineBytes+64 {
		t.Fatalf("line preview is unexpectedly large: %d bytes", len(matches[0].lineText))
	}
}

func TestSearchFilesPropagatesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := searchFiles(ctx, "needle", t.TempDir(), "", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func TestSearchFilesWithRegexIgnoresOnlyPathsBelowRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(path, []byte("needle"), 0o644); err != nil {
		t.Fatal(err)
	}
	matches, truncated, err := searchFilesWithRegex(context.Background(), "needle", root, "", 10)
	if err != nil || truncated || len(matches) != 1 || matches[0].path != path {
		t.Fatalf("matches=%v, truncated=%v, err=%v; want %s", matches, truncated, err, path)
	}
}

func TestSearchWithRipgrepPreservesNewlinesInFilenames(t *testing.T) {
	if !process.Exists("rg") {
		t.Skip("ripgrep is unavailable")
	}
	root := t.TempDir()
	path := filepath.Join(root, "line\nbreak.txt")
	if err := os.WriteFile(path, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRoot, filepath.Base(path))
	matches, truncated, err := searchWithRipgrep(context.Background(), "needle", root, "", 10)
	if err != nil || truncated || len(matches) != 1 || matches[0].path != want || matches[0].lineText != "needle" {
		t.Fatalf("matches=%v, truncated=%v, err=%v; want %s", matches, truncated, err, want)
	}
}

func BenchmarkGrepTopK(b *testing.B) {
	candidates := make([]grepMatch, 10_000)
	for index := range candidates {
		candidates[index] = grepMatch{
			path:    fmt.Sprintf("file-%06d.go", index),
			modTime: time.Unix(int64(index), 0),
			lineNum: 1,
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		selection := make(grepMatchHeap, 0, 100)
		for _, candidate := range candidates {
			selection.Offer(candidate, 100)
		}
		if selection.Len() != 100 {
			b.Fatalf("selection size = %d", selection.Len())
		}
	}
}
