package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestGlobWithDoublestarLimitsAndOrdersCandidates(t *testing.T) {
	root := t.TempDir()
	for index := 1; index <= 5; index++ {
		path := filepath.Join(root, fmt.Sprintf("file%d.go", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		modified := time.Unix(int64(index)*60, 0)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		limit     int
		indexes   []int
		truncated bool
	}{
		{limit: 1, indexes: []int{5}, truncated: true},
		{limit: 2, indexes: []int{5, 4}, truncated: true},
		{limit: 3, indexes: []int{5, 4, 3}, truncated: true},
		{limit: 5, indexes: []int{5, 4, 3, 2, 1}},
		{limit: 0, indexes: []int{5, 4, 3, 2, 1}},
	} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			matches, truncated, err := GlobWithDoublestar("*.go", root, tc.limit)
			if err != nil {
				t.Fatalf("glob returned an error: %v", err)
			}
			want := make([]string, len(tc.indexes))
			for index, file := range tc.indexes {
				want[index] = filepath.Join(root, fmt.Sprintf("file%d.go", file))
			}
			if !reflect.DeepEqual(matches, want) || truncated != tc.truncated {
				t.Fatalf("glob = %v, truncated=%v; want %v, truncated=%v", matches, truncated, want, tc.truncated)
			}
		})
	}
}

func TestGlobWithDoublestarUsesStableTiesAndIgnoresSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(root, "c.go"), filepath.Join(root, "a.go"), filepath.Join(root, "b.go"), filepath.Join(outside, "outside.go")} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(60, 0), time.Unix(60, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(root, "0.go")); err != nil {
		t.Fatal(err)
	}
	matches, truncated, err := GlobWithDoublestar("**/*.go", root, 2)
	want := []string{filepath.Join(root, "a.go"), filepath.Join(root, "b.go")}
	if err != nil || !truncated || !reflect.DeepEqual(matches, want) {
		t.Fatalf("glob = %v, truncated=%v, err=%v; want %v", matches, truncated, err, want)
	}
}

func TestGlobWithDoublestarJoinsSearchRoot(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir("src", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("src", "src.go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, searchRoot := range []string{"src", "./src", filepath.Join(root, "src")} {
		matches, truncated, err := GlobWithDoublestar("*.go", searchRoot, 10)
		want := []string{filepath.Join(searchRoot, "src.go")}
		if err != nil || truncated || !reflect.DeepEqual(matches, want) {
			t.Errorf("root %q: got %v, truncated=%v, err=%v; want %v", searchRoot, matches, truncated, err, want)
		}
	}
	matches, truncated, err := GlobWithDoublestar("src/*.go", ".", 10)
	if err != nil || truncated || !reflect.DeepEqual(matches, []string{filepath.Join("src", "src.go")}) {
		t.Fatalf("current-directory root: got %v, truncated=%v, err=%v", matches, truncated, err)
	}
}

func TestGlobWithDoublestarPreservesPatternErrors(t *testing.T) {
	if _, _, err := GlobWithDoublestar("[", t.TempDir(), 1); err == nil {
		t.Fatal("invalid glob pattern should fail")
	}
}
