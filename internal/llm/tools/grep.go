package tools

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

type GrepParams struct {
	Pattern     string `json:"pattern"`
	Path        string `json:"path"`
	Include     string `json:"include"`
	LiteralText bool   `json:"literal_text"`
}

type grepMatch struct {
	path     string
	modTime  time.Time
	lineNum  int
	lineText string
}

type GrepResponseMetadata struct {
	NumberOfMatches int  `json:"number_of_matches"`
	Truncated       bool `json:"truncated"`
}

type grepTool struct{}

const (
	maxGrepCaptureBytes  = 1 << 20
	maxGrepLineBytes     = 2000
	maxGrepScanLineBytes = 2 << 20

	GrepToolName    = "grep"
	grepDescription = `Fast content search tool that finds files containing specific text or patterns, returning matching file paths sorted by modification time (newest first).

WHEN TO USE THIS TOOL:
- Use when you need to find files containing specific text or patterns
- Great for searching code bases for function names, variable declarations, or error messages
- Useful for finding all files that use a particular API or pattern

HOW TO USE:
- Provide a regex pattern to search for within file contents
- Set literal_text=true if you want to search for the exact text with special characters (recommended for non-regex users)
- Optionally specify a starting directory (defaults to current working directory)
- Optionally provide an include pattern to filter which files to search
- Results are sorted with most recently modified files first

REGEX PATTERN SYNTAX (when literal_text=false):
- Supports standard regular expression syntax
- 'function' searches for the literal text "function"
- 'log\..*Error' finds text starting with "log." and ending with "Error"
- 'import\s+.*\s+from' finds import statements in JavaScript/TypeScript

COMMON INCLUDE PATTERN EXAMPLES:
- '*.js' - Only search JavaScript files
- '*.{ts,tsx}' - Only search TypeScript files
- '*.go' - Only search Go files

LIMITATIONS:
- Results are limited to 100 files (newest first)
- Performance depends on the number of files being searched
- Very large binary files may be skipped
- Hidden files (starting with '.') are skipped

TIPS:
- For faster, more targeted searches, first use Glob to find relevant files, then use Grep
- When doing iterative exploration that may require multiple rounds of searching, consider using the Agent tool instead
- Always check if results are truncated and refine your search pattern if needed
- Use literal_text=true when searching for exact text containing special characters like dots, parentheses, etc.`
)

func NewGrepTool() BaseTool {
	return &grepTool{}
}

func (g *grepTool) Info() ToolInfo {
	return ToolInfo{
		Name:        GrepToolName,
		Description: grepDescription,
		Parameters: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "The regex pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "The directory to search in. Defaults to the current working directory.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "File pattern to include in the search (e.g. \"*.js\", \"*.{ts,tsx}\")",
			},
			"literal_text": map[string]any{
				"type":        "boolean",
				"description": "If true, the pattern will be treated as literal text with special regex characters escaped. Default is false.",
			},
		},
		Required: []string{"pattern"},
	}
}

// escapeRegexPattern escapes special regex characters so they're treated as literal characters
func escapeRegexPattern(pattern string) string {
	return regexp.QuoteMeta(pattern)
}

func (g *grepTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params GrepParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.Pattern == "" {
		return NewTextErrorResponse("pattern is required"), nil
	}

	// If literal_text is true, escape the pattern
	searchPattern := params.Pattern
	if params.LiteralText {
		searchPattern = escapeRegexPattern(params.Pattern)
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = "."
	}
	resolvedSearchPath, err := resolveWorkspaceToolPath(searchPath)
	if err != nil {
		return NewTextErrorResponse("path is outside the workspace: " + err.Error()), nil
	}

	matches, truncated, err := searchFiles(ctx, searchPattern, resolvedSearchPath, params.Include, 100)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error searching files: %w", err)
	}

	var output string
	if len(matches) == 0 {
		output = "No files found"
	} else {
		output = fmt.Sprintf("Found %d matches\n", len(matches))

		currentFile := ""
		for _, match := range matches {
			if currentFile != match.path {
				if currentFile != "" {
					output += "\n"
				}
				currentFile = match.path
				output += fmt.Sprintf("%s:\n", match.path)
			}
			if match.lineNum > 0 {
				output += fmt.Sprintf("  Line %d: %s\n", match.lineNum, match.lineText)
			} else {
				output += fmt.Sprintf("  %s\n", match.path)
			}
		}

		if truncated {
			output += "\n(Results are truncated. Consider using a more specific path or pattern.)"
		}
	}

	return WithResponseMetadata(
		NewTextResponse(output),
		GrepResponseMetadata{
			NumberOfMatches: len(matches),
			Truncated:       truncated,
		},
	), nil
}

var errRipgrepUnavailable = errors.New("ripgrep unavailable")

func searchFiles(ctx context.Context, pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	matches, truncated, err := searchWithRipgrep(ctx, pattern, rootPath, include, limit)
	if err == nil {
		return matches, truncated, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if !errors.Is(err, errRipgrepUnavailable) {
		return nil, false, err
	}
	return searchFilesWithRegex(ctx, pattern, rootPath, include, limit)
}

func searchWithRipgrep(ctx context.Context, pattern, path, include string, limit int) ([]grepMatch, bool, error) {
	rgPath, err := process.LookPath("rg")
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", errRipgrepUnavailable, err)
	}

	// One result per file keeps ripgrep and the Go fallback consistent. Reverse
	// modification ordering means the bounded output prefix is already top-K.
	args := []string{
		"--line-number",
		"--max-count", "1",
		"--sortr", "modified",
		"--null",
		"--max-columns", strconv.Itoa(maxGrepLineBytes),
		"--max-columns-preview",
		"--color", "never",
		"--no-heading",
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	args = append(args, "--", pattern, path)

	result, err := process.RunOutput(ctx, process.Spec{
		Argv:           append([]string{rgPath}, args...),
		Directory:      path,
		MaxOutputBytes: maxGrepCaptureBytes,
	})
	output := result.Output
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, false, ctxErr
	}
	if err != nil && !errors.Is(err, process.ErrOutputLimitExceeded) {
		if result.ExitCode == 1 {
			return []grepMatch{}, false, nil
		}
		return nil, false, err
	}

	capacity := limit + 1
	if capacity <= 1 {
		capacity = 101
	}
	matches := make([]grepMatch, 0, min(bytes.Count(output, []byte{0}), capacity))

	for len(output) > 0 {
		// Read the NUL-terminated path before finding the source-line newline:
		// filenames may themselves contain newlines as well as colons.
		nul := bytes.IndexByte(output, 0)
		if nul <= 0 {
			break
		}
		filePath := string(output[:nul])
		output = output[nul+1:]
		lineEnd := bytes.IndexByte(output, '\n')
		if lineEnd < 0 {
			// A bounded capture can stop mid-record. Do not expose a partial
			// path or source line as a complete match.
			break
		}
		remainder := output[:lineEnd]
		output = output[lineEnd+1:]
		separator := bytes.IndexByte(remainder, ':')
		if separator <= 0 {
			continue
		}
		lineNum, err := strconv.Atoi(string(remainder[:separator]))
		if err != nil {
			continue
		}
		lineText := truncateGrepLine(string(remainder[separator+1:]))

		resolvedFilePath, resolveErr := security.ResolveWorkspacePath(path, filePath)
		if resolveErr != nil {
			continue
		}

		fileInfo, err := os.Stat(resolvedFilePath)
		if err != nil {
			continue // Skip files we can't access
		}

		matches = append(matches, grepMatch{
			path:     resolvedFilePath,
			modTime:  fileInfo.ModTime(),
			lineNum:  lineNum,
			lineText: lineText,
		})
		if len(matches) >= capacity {
			break
		}
	}

	sortGrepMatches(matches)
	truncated := result.OutputTruncated || (limit > 0 && len(matches) > limit)
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, truncated, nil
}

func searchFilesWithRegex(ctx context.Context, pattern, rootPath, include string, limit int) ([]grepMatch, bool, error) {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, false, fmt.Errorf("invalid regex pattern: %w", err)
	}
	if include != "" {
		if _, matchErr := doublestar.Match(filepath.ToSlash(include), "probe"); matchErr != nil {
			return nil, false, fmt.Errorf("invalid include pattern: %w", matchErr)
		}
	}

	keep := limit
	if keep <= 0 {
		keep = 100
	}
	selection := make(grepMatchHeap, 0, keep)
	heap.Init(&selection)
	totalMatches := 0

	err = filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // Skip errors
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		resolvedPath := path
		relativePath, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return relErr
		}

		if entry.IsDir() {
			if path != rootPath && fileutil.SkipHidden(relativePath) {
				return filepath.SkipDir
			}
			return nil // Skip directories
		}

		if fileutil.SkipHidden(relativePath) {
			return nil
		}

		if include != "" {
			included, includeErr := matchesGrepInclude(rootPath, resolvedPath, include)
			if includeErr != nil {
				return includeErr
			}
			if !included {
				return nil
			}
		}

		match, lineNum, lineText, err := fileContainsPattern(ctx, resolvedPath, regex)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return nil // Skip files we can't read
		}

		if match {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil
			}
			totalMatches++
			selection.Offer(grepMatch{
				path:     resolvedPath,
				modTime:  info.ModTime(),
				lineNum:  lineNum,
				lineText: truncateGrepLine(lineText),
			}, keep)
		}

		return nil
	})
	if err != nil {
		return nil, false, err
	}

	matches := append([]grepMatch(nil), selection...)
	sortGrepMatches(matches)
	return matches, totalMatches > keep, nil
}

func fileContainsPattern(ctx context.Context, filePath string, pattern *regexp.Regexp) (bool, int, string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return false, 0, "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxGrepScanLineBytes)
	lineNum := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return false, 0, "", err
		}
		lineNum++
		line := scanner.Text()
		if pattern.MatchString(line) {
			return true, lineNum, line, nil
		}
	}

	return false, 0, "", scanner.Err()
}

func matchesGrepInclude(rootPath, path, pattern string) (bool, error) {
	relative, err := filepath.Rel(rootPath, path)
	if err != nil {
		return false, err
	}
	pattern = filepath.ToSlash(pattern)
	relative = filepath.ToSlash(relative)
	matched, err := doublestar.Match(pattern, relative)
	if err != nil || matched || strings.Contains(pattern, "/") {
		return matched, err
	}
	return doublestar.Match(pattern, filepath.Base(relative))
}

func truncateGrepLine(line string) string {
	if len(line) <= maxGrepLineBytes {
		return line
	}
	line = line[:maxGrepLineBytes]
	for len(line) > 0 && !utf8.ValidString(line) {
		line = line[:len(line)-1]
	}
	return line + "..."
}

func sortGrepMatches(matches []grepMatch) {
	sort.Slice(matches, func(i, j int) bool { return grepMatchBefore(matches[i], matches[j]) })
}

func grepMatchBefore(left, right grepMatch) bool {
	if !left.modTime.Equal(right.modTime) {
		return left.modTime.After(right.modTime)
	}
	if left.path != right.path {
		return left.path < right.path
	}
	return left.lineNum < right.lineNum
}

// grepMatchHeap keeps the oldest/worst match at the root so a scan retains
// only the newest K entries instead of collecting every result in memory.
type grepMatchHeap []grepMatch

func (h grepMatchHeap) Len() int           { return len(h) }
func (h grepMatchHeap) Less(i, j int) bool { return grepMatchBefore(h[j], h[i]) }
func (h grepMatchHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *grepMatchHeap) Push(value any)    { *h = append(*h, value.(grepMatch)) }
func (h *grepMatchHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func (h *grepMatchHeap) Offer(candidate grepMatch, limit int) {
	if limit <= 0 {
		return
	}
	if h.Len() < limit {
		heap.Push(h, candidate)
		return
	}
	if grepMatchBefore(candidate, (*h)[0]) {
		// Replacing the root and repairing the heap avoids boxing candidate
		// twice through heap.Pop/Push for every retained match.
		(*h)[0] = candidate
		heap.Fix(h, 0)
	}
}
