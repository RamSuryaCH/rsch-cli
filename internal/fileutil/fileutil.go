package fileutil

import (
	"container/heap"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/process"
)

var (
	rgPath  string
	fzfPath string
)

func init() {
	var err error
	rgPath, err = process.LookPath("rg")
	if err != nil {
		logging.Warn("Ripgrep (rg) not found in $PATH. Some features might be limited or slower.")
		rgPath = ""
	}
	fzfPath, err = process.LookPath("fzf")
	if err != nil {
		// FZF is an optional completion accelerator. Keep the normal CLI quiet;
		// the fallback path is already handled by the completion service.
		logging.Debug("FZF not found in $PATH; using completion fallback")
		fzfPath = ""
	}
}

func GetRgSpec(globPattern string) (process.Spec, bool) {
	if rgPath == "" {
		return process.Spec{}, false
	}
	rgArgs := []string{
		"--files",
		"-L",
		"--null",
	}
	if globPattern != "" {
		if !filepath.IsAbs(globPattern) && !strings.HasPrefix(globPattern, "/") {
			globPattern = "/" + globPattern
		}
		rgArgs = append(rgArgs, "--glob", globPattern)
	}
	return process.Spec{Argv: append([]string{rgPath}, rgArgs...), Directory: "."}, true
}

func GetFzfSpec(query string) (process.Spec, bool) {
	if fzfPath == "" {
		return process.Spec{}, false
	}
	fzfArgs := []string{
		"--filter",
		query,
		"--read0",
		"--print0",
	}
	return process.Spec{Argv: append([]string{fzfPath}, fzfArgs...), Directory: "."}, true
}

type FileInfo struct {
	Path    string
	ModTime time.Time
}

// oldestFiles keeps the least preferred retained result at the root, so a
// limited search stores only its newest matches while examining every file.
type oldestFiles []FileInfo

func (h oldestFiles) Len() int           { return len(h) }
func (h oldestFiles) Less(i, j int) bool { return newerFile(h[j], h[i]) }
func (h oldestFiles) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *oldestFiles) Push(value any)    { *h = append(*h, value.(FileInfo)) }
func (h *oldestFiles) Pop() any {
	last := (*h)[len(*h)-1]
	*h = (*h)[:len(*h)-1]
	return last
}

func newerFile(a, b FileInfo) bool {
	if a.ModTime.Equal(b.ModTime) {
		return a.Path < b.Path
	}
	return a.ModTime.After(b.ModTime)
}

func SkipHidden(path string) bool {
	// Check for hidden files (starting with a dot)
	base := filepath.Base(path)
	if base != "." && strings.HasPrefix(base, ".") {
		return true
	}

	commonIgnoredDirs := map[string]bool{
		".opencode":        true,
		".rsch":            true,
		"node_modules":     true,
		"vendor":           true,
		"dist":             true,
		"build":            true,
		"target":           true,
		".git":             true,
		".idea":            true,
		".vscode":          true,
		"__pycache__":      true,
		"bin":              true,
		"obj":              true,
		"out":              true,
		"coverage":         true,
		"tmp":              true,
		"temp":             true,
		"logs":             true,
		"generated":        true,
		"bower_components": true,
		"jspm_packages":    true,
	}

	parts := strings.Split(path, string(os.PathSeparator))
	for _, part := range parts {
		if commonIgnoredDirs[part] {
			return true
		}
	}
	return false
}

func GlobWithDoublestar(pattern, searchPath string, limit int) ([]string, bool, error) {
	fsys := os.DirFS(searchPath)
	relPattern := strings.TrimPrefix(pattern, "/")
	var matches oldestFiles
	truncated := false

	err := doublestar.GlobWalk(fsys, relPattern, func(path string, d fs.DirEntry) error {
		if d.IsDir() {
			if SkipHidden(path) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || SkipHidden(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// GlobWalk paths are always relative to fsys, including when their
		// names happen to start with the same text as the search directory.
		candidate := FileInfo{Path: filepath.Join(searchPath, path), ModTime: info.ModTime()}
		if limit <= 0 {
			matches = append(matches, candidate)
		} else if len(matches) < limit {
			heap.Push(&matches, candidate)
		} else {
			truncated = true
			if newerFile(candidate, matches[0]) {
				matches[0] = candidate
				heap.Fix(&matches, 0)
			}
		}
		return nil
	}, doublestar.WithNoFollow())
	if err != nil {
		return nil, false, fmt.Errorf("glob walk error: %w", err)
	}

	sort.Slice(matches, func(i, j int) bool {
		return newerFile(matches[i], matches[j])
	})

	results := make([]string, len(matches))
	for i, m := range matches {
		results[i] = m.Path
	}
	return results, truncated, nil
}
