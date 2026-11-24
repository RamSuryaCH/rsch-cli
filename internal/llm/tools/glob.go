package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/process"
)

const (
	maxGlobCaptureBytes = 1 << 20

	GlobToolName    = "glob"
	globDescription = `Fast file pattern matching tool that finds files by name and pattern, returning matching paths sorted by modification time (newest first).

WHEN TO USE THIS TOOL:
- Use when you need to find files by name patterns or extensions
- Great for finding specific file types across a directory structure
- Useful for discovering files that match certain naming conventions

HOW TO USE:
- Provide a glob pattern to match against file paths
- Optionally specify a starting directory (defaults to current working directory)
- Results are sorted with most recently modified files first

GLOB PATTERN SYNTAX:
- '*' matches any sequence of non-separator characters
- '**' matches any sequence of characters, including separators
- '?' matches any single non-separator character
- '[...]' matches any character in the brackets
- '[!...]' matches any character not in the brackets

COMMON PATTERN EXAMPLES:
- '*.js' - Find all JavaScript files in the current directory
- '**/*.js' - Find all JavaScript files in any subdirectory
- 'src/**/*.{ts,tsx}' - Find all TypeScript files in the src directory
- '*.{html,css,js}' - Find all HTML, CSS, and JS files

LIMITATIONS:
- Results are limited to 100 files (newest first)
- Does not search file contents (use Grep tool for that)
- Hidden files (starting with '.') are skipped

TIPS:
- For the most useful results, combine with the Grep tool: first find files with Glob, then search their contents with Grep
- When doing iterative exploration that may require multiple rounds of searching, consider using the Agent tool instead
- Always check if results are truncated and refine your search pattern if needed`
)

type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

type globTool struct{}

func NewGlobTool() BaseTool {
	return &globTool{}
}

func (g *globTool) Info() ToolInfo {
	return ToolInfo{
		Name:        GlobToolName,
		Description: globDescription,
		Parameters: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The glob pattern to match files against",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to the current working directory.",
			},
		},
		Required: []string{"pattern"},
	}
}

func (g *globTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GlobParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}
	if err := validateWorkspaceGlobPattern(params.Pattern); err != nil {
		return NewTextErrorResponse("glob pattern is outside the workspace: " + err.Error()), nil
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = config.WorkingDirectory()
	}
	resolvedSearchPath, err := resolveWorkspaceToolPath(searchPath)
	if err != nil {
		return NewTextErrorResponse("path is outside the workspace: " + err.Error()), nil
	}

	files, truncated, err := globFiles(ctx, params.Pattern, resolvedSearchPath, 100)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error finding files: %w", err)
	}

	var output string
	if len(files) == 0 {
		output = "No files found"
	} else {
		output = strings.Join(files, "\n")
		if truncated {
			output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return WithResponseMetadata(
		NewTextResponse(output),
		GlobResponseMetadata{
			NumberOfFiles: len(files),
			Truncated:     truncated,
		},
	), nil
}

func globFiles(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	rgSpec, ok := fileutil.GetRgSpec(pattern)
	if ok {
		rgSpec.Directory = searchPath
		// Match the advertised newest-first ordering before bounding captured
		// output, and do not traverse symlink targets outside the search tree.
		rgSpec.Argv = append(rgSpec.Argv, "--sortr", "modified", "--no-follow")
		rgSpec.MaxOutputBytes = maxGlobCaptureBytes
		matches, truncated, err := runRipgrep(ctx, rgSpec, searchPath, limit)
		if err == nil {
			canonical, canonicalErr := canonicalWorkspaceMatches(matches)
			if canonicalErr != nil {
				return nil, false, canonicalErr
			}
			return canonical, truncated, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		logging.Warn(fmt.Sprintf("Ripgrep execution failed: %v. Falling back to doublestar.", err))
	}

	matches, truncated, err := fileutil.GlobWithDoublestar(pattern, searchPath, limit)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if err != nil {
		return nil, false, err
	}
	canonical, err := canonicalWorkspaceMatches(matches)
	if err != nil {
		return nil, false, err
	}
	return canonical, truncated, nil
}

func validateWorkspaceGlobPattern(pattern string) error {
	if filepath.IsAbs(pattern) || strings.ContainsRune(pattern, '\x00') {
		return fmt.Errorf("absolute paths and control characters are not allowed")
	}
	for _, part := range strings.FieldsFunc(filepath.ToSlash(pattern), func(r rune) bool { return r == '/' }) {
		if part == ".." {
			return fmt.Errorf("parent-directory traversal is not allowed")
		}
	}
	return nil
}

func canonicalWorkspaceMatches(matches []string) ([]string, error) {
	canonical := make([]string, 0, len(matches))
	for _, match := range matches {
		path, err := resolveWorkspaceToolPath(match)
		if err != nil {
			return nil, fmt.Errorf("glob result %q is outside the workspace: %w", match, err)
		}
		canonical = append(canonical, path)
	}
	return canonical, nil
}

func runRipgrep(ctx context.Context, spec process.Spec, searchRoot string, limit int) ([]string, bool, error) {
	result, err := process.RunOutput(ctx, spec)
	out := result.Output
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if err != nil && !errors.Is(err, process.ErrOutputLimitExceeded) {
		if result.ExitCode == 1 {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("ripgrep: %w\n%s", err, out)
	}

	records := bytes.Split(out, []byte{0})
	if result.OutputTruncated && len(out) > 0 && out[len(out)-1] != 0 {
		// Never treat an output cap in the middle of a filename as a match.
		records = records[:len(records)-1]
	}
	var matches []fileutil.FileInfo
	for _, p := range records {
		if len(p) == 0 {
			continue
		}
		absPath := string(p)
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(searchRoot, absPath)
		}
		relative, err := filepath.Rel(searchRoot, absPath)
		if err != nil || fileutil.SkipHidden(relative) {
			continue
		}
		info, err := os.Stat(absPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		matches = append(matches, fileutil.FileInfo{Path: absPath, ModTime: info.ModTime()})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].ModTime.Equal(matches[j].ModTime) {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].ModTime.After(matches[j].ModTime)
	})

	truncated := result.OutputTruncated || (limit > 0 && len(matches) > limit)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	paths := make([]string, len(matches))
	for index, match := range matches {
		paths[index] = match.Path
	}
	return paths, truncated, nil
}
