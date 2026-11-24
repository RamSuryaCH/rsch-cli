package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestGlobFilesNewestFirstAndActualTruncation(t *testing.T) {
	if !process.Exists("rg") {
		t.Skip("ripgrep is unavailable")
	}
	for _, directory := range []string{"project", "tmp"} {
		t.Run(directory, func(t *testing.T) {
			workspace, _ := boundaryWorkspace(t)
			root := filepath.Join(workspace, directory)
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			root, err := resolveWorkspaceToolPath(root)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{"a.go", "middle.go", "newest-long-name.go"}
			for index, name := range names {
				path := filepath.Join(root, name)
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
				stamp := time.Unix(1_700_000_000+int64(index), 0)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			for _, limit := range []int{1, 3, 4} {
				matches, truncated, err := globFiles(context.Background(), "*.go", root, limit)
				want := []string{filepath.Join(root, names[2]), filepath.Join(root, names[1]), filepath.Join(root, names[0])}
				if limit < len(want) {
					want = want[:limit]
				}
				if err != nil || truncated != (limit < len(names)) || !reflect.DeepEqual(matches, want) {
					t.Fatalf("limit %d: got %v, truncated=%v, err=%v; want %v", limit, matches, truncated, err, want)
				}
			}
		})
	}
}

func TestGlobFilesHonorsCancellation(t *testing.T) {
	workspace, _ := boundaryWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := globFiles(ctx, "*.go", workspace, 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context cancellation", err)
	}
}

func TestGlobFilesSkipsOutsideSymlink(t *testing.T) {
	if !process.Exists("rg") {
		t.Skip("ripgrep is unavailable")
	}
	workspace, outside := boundaryWorkspace(t)
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(workspace, "escape.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	valid := filepath.Join(workspace, "valid.go")
	if err := os.WriteFile(valid, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := resolveWorkspaceToolPath(valid)
	if err != nil {
		t.Fatal(err)
	}
	matches, truncated, err := globFiles(context.Background(), "*.go", workspace, 10)
	if err != nil || truncated || !reflect.DeepEqual(matches, []string{want}) {
		t.Fatalf("matches = %v, truncated=%v, err=%v; want only %s", matches, truncated, err, want)
	}
}

func TestRunRipgrepDropsPartialFilename(t *testing.T) {
	rgPath, err := process.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is unavailable")
	}
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	matches, truncated, err := runRipgrep(context.Background(), process.Spec{
		Argv:           []string{rgPath, "--files", "--null", "--sort", "path"},
		Directory:      root,
		MaxOutputBytes: 8, // "a.go\x00b.g": the second filename is incomplete.
	}, root, 10)
	if err != nil || !truncated || !reflect.DeepEqual(matches, []string{filepath.Join(root, "a.go")}) {
		t.Fatalf("matches=%v, truncated=%v, err=%v; want only complete first record", matches, truncated, err)
	}
}
