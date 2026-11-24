package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	agentsFileName         = "AGENTS.md"
	agentsOverrideFileName = "AGENTS.override.md"
)

var configuredInstructionNames = map[string]struct{}{
	".cursorrules":            {},
	"claude.md":               {},
	"claude.local.md":         {},
	"copilot-instructions.md": {},
	"opencode.md":             {},
	"opencode.local.md":       {},
	"rsch.md":                 {},
	"rsch.local.md":           {},
}

// collectHierarchicalInstructionFiles discovers one instruction file per
// directory from the repository root to the configured working directory.
// AGENTS.override.md replaces AGENTS.md in the same directory, while a nearer
// directory is appended after (and therefore takes precedence over) its
// ancestors. Files are resolved on every call so prompt construction never
// relies on stale instruction contents or discovery results.
func collectHierarchicalInstructionFiles(workDir string) []string {
	files, _ := DiscoverInstructionFiles(workDir)
	return files
}

// DiscoverInstructionFiles returns the canonical instruction files that apply
// to workDir in increasing precedence order. Callers can use the ordered paths
// for explain/debug surfaces without duplicating the discovery rules used by
// prompt construction.
func DiscoverInstructionFiles(workDir string) ([]string, error) {
	target, err := canonicalDirectory(workDir)
	if err != nil {
		return nil, err
	}
	root := instructionRoot(target)
	directories := directoriesFromRoot(root, target)
	files := make([]string, 0, len(directories))
	for _, directory := range directories {
		if file, found := instructionFile(directory, root); found {
			files = append(files, file)
		}
	}
	return files, nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve instruction working directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve instruction working directory symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect instruction working directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("instruction working directory is not a directory: %s", canonical)
	}
	return filepath.Clean(canonical), nil
}

// instructionRoot uses the nearest Git worktree boundary. If the working
// directory is not inside a Git worktree, instruction discovery is confined to
// that directory rather than walking arbitrary filesystem ancestors.
func instructionRoot(target string) string {
	for current := target; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return target
		}
	}
}

func directoriesFromRoot(root, target string) []string {
	reversed := make([]string, 0, 4)
	for current := target; ; current = filepath.Dir(current) {
		if !pathWithin(root, current) {
			return nil
		}
		reversed = append(reversed, current)
		if filepath.Clean(current) == filepath.Clean(root) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func instructionFile(directory, root string) (string, bool) {
	for _, name := range []string{agentsOverrideFileName, agentsFileName} {
		candidate := filepath.Join(directory, name)
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil || !pathWithin(root, canonical) {
			continue
		}
		info, err := os.Stat(canonical)
		if err == nil && info.Mode().IsRegular() {
			return filepath.Clean(canonical), true
		}
	}
	return "", false
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isConfiguredInstructionFile(workDir, file string) bool {
	name := strings.ToLower(filepath.Base(file))
	if _, known := configuredInstructionNames[name]; known {
		return true
	}
	return isCursorRuleFile(workDir, file)
}

func isAgentsInstructionFile(file string) bool {
	name := filepath.Base(file)
	return strings.EqualFold(name, agentsFileName) || strings.EqualFold(name, agentsOverrideFileName)
}

func mergeContextFiles(groups ...[]string) []string {
	count := 0
	for _, group := range groups {
		count += len(group)
	}
	files := make([]string, 0, count)
	for _, group := range groups {
		files = append(files, group...)
	}
	return dedupeContextFiles(files)
}

func dedupeContextFiles(files []string) []string {
	unique := make([]string, 0, len(files))
	for _, file := range files {
		duplicate := false
		for _, existing := range unique {
			if sameContextFile(existing, file) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		unique = append(unique, file)
	}
	return unique
}

func sameContextFile(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	return contextPathKey(left) == contextPathKey(right)
}

func contextPathKey(file string) string {
	cleaned := filepath.Clean(file)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}
