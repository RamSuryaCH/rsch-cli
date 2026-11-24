package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/opencode-ai/opencode/internal/visual"
)

type ViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

type viewTool struct {
	lspClients lsp.ClientProvider
}

type ViewResponseMetadata struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

const (
	ViewToolName     = "view"
	MaxReadSize      = 250 * 1024
	DefaultReadLimit = 2000
	MaxLineLength    = 2000
	viewDescription  = `File viewing tool that reads and displays the contents of files with line numbers, allowing you to examine code, logs, or text data.

WHEN TO USE THIS TOOL:
- Use when you need to read the contents of a specific file
- Helpful for examining source code, configuration files, or log files
- Perfect for looking at text-based file formats

HOW TO USE:
- Provide the path to the file you want to view
- Optionally specify an offset to start reading from a specific line
- Optionally specify a limit to control how many lines are read

FEATURES:
- Displays file contents with line numbers for easy reference
- Can read from any position in a file using the offset parameter
- Handles large files by limiting the number of lines read
- Automatically truncates very long lines for better display
- Suggests similar file names when the requested file isn't found

LIMITATIONS:
- Maximum file size is 250KB
- Default reading limit is 2000 lines
- Lines longer than 2000 characters are truncated
- Binary files are not rendered as text
- Images return verified dimensions, format, size, dominant colors, and repository hints

TIPS:
- Use with Glob tool to first find files you want to view
- For code exploration, first use Grep to find relevant files, then View to examine them
- When viewing large files, use the offset parameter to read specific sections`
)

func NewViewTool(lspClients map[string]*lsp.Client) BaseTool {
	return NewViewToolWithClientProvider(lsp.NewStaticClientProvider(lspClients))
}

func NewViewToolWithClientProvider(lspClients lsp.ClientProvider) BaseTool {
	return &viewTool{
		lspClients,
	}
}

func resolveWorkspaceToolPath(path string) (string, error) {
	return security.ResolveWorkspacePath(config.WorkingDirectory(), path)
}

func (v *viewTool) Info() ToolInfo {
	return ToolInfo{
		Name:        ViewToolName,
		Description: viewDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to read",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "The line number to start reading from (0-based)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "The number of lines to read (defaults to 2000)",
			},
		},
		Required: []string{"file_path"},
	}
}

// Run implements Tool.
func (v *viewTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params ViewParams
	logging.Debug("view tool params", "params", call.Input)
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse(fmt.Sprintf("error parsing parameters: %s", err)), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}
	if params.Offset < 0 {
		return NewTextErrorResponse("offset must be non-negative"), nil
	}

	filePath, err := resolveWorkspaceToolPath(params.FilePath)
	if err != nil {
		return NewTextErrorResponse("file path is outside the workspace: " + err.Error()), nil
	}

	// Check if file exists
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to offer suggestions for similarly named files
			dir := filepath.Dir(filePath)
			base := filepath.Base(filePath)

			dirEntries, dirErr := os.ReadDir(dir)
			if dirErr == nil {
				var suggestions []string
				for _, entry := range dirEntries {
					if strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(base)) ||
						strings.Contains(strings.ToLower(base), strings.ToLower(entry.Name())) {
						suggestions = append(suggestions, filepath.Join(dir, entry.Name()))
						if len(suggestions) >= 3 {
							break
						}
					}
				}

				if len(suggestions) > 0 {
					return NewTextErrorResponse(fmt.Sprintf("File not found: %s\n\nDid you mean one of these?\n%s",
						filePath, strings.Join(suggestions, "\n"))), nil
				}
			}

			return NewTextErrorResponse(fmt.Sprintf("File not found: %s", filePath)), nil
		}
		return ToolResponse{}, fmt.Errorf("error accessing file: %w", err)
	}

	// Check if it's a directory
	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
	}

	// Check file size
	if fileInfo.Size() > MaxReadSize {
		return NewTextErrorResponse(fmt.Sprintf("File is too large (%d bytes). Maximum size is %d bytes",
			fileInfo.Size(), MaxReadSize)), nil
	}

	// Set default limit if not provided
	if params.Limit <= 0 {
		params.Limit = DefaultReadLimit
	}

	// Check if it's an image file
	isImage, imageType := isImageFile(filePath)
	if isImage {
		report, analyzeErr := visual.Analyze(filePath, config.WorkingDirectory())
		if analyzeErr != nil {
			return ToolResponse{}, fmt.Errorf("error analyzing %s image: %w", imageType, analyzeErr)
		}
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return ToolResponse{}, fmt.Errorf("error encoding image evidence: %w", marshalErr)
		}
		return WithResponseMetadata(
			NewTextResponse("<image-evidence>\n"+string(data)+"\n</image-evidence>"),
			ViewResponseMetadata{FilePath: filePath, Content: string(data)},
		), nil
	}

	// Read the file content
	content, lineCount, err := readTextFile(filePath, params.Offset, params.Limit)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("error reading file: %w", err)
	}

	lsps := v.lspClients.Snapshot()
	notifyLspOpenFile(ctx, filePath, lsps)
	output := "<file>\n"
	// Format the output with line numbers
	displayedLines := min(params.Limit, max(0, lineCount-params.Offset))
	if displayedLines > 0 {
		output += addLineNumbers(content, params.Offset+1)
	}

	// Add a note if the content was truncated
	if lineCount-params.Offset > displayedLines {
		output += fmt.Sprintf("\n\n(File has more lines. Use 'offset' parameter to read beyond line %d)",
			params.Offset+displayedLines)
	}
	output += "\n</file>\n"
	output += getDiagnostics(filePath, lsps)
	recordFileRead(ctx, filePath)
	return WithResponseMetadata(
		NewTextResponse(output),
		ViewResponseMetadata{
			FilePath: filePath,
			Content:  content,
		},
	), nil
}

func addLineNumbers(content string, startLine int) string {
	lines := strings.Split(content, "\n")

	var result []string
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		lineNum := i + startLine
		numStr := fmt.Sprintf("%d", lineNum)

		if len(numStr) >= 6 {
			result = append(result, fmt.Sprintf("%s|%s", numStr, line))
		} else {
			paddedNum := fmt.Sprintf("%6s", numStr)
			result = append(result, fmt.Sprintf("%s|%s", paddedNum, line))
		}
	}

	return strings.Join(result, "\n")
}

func readTextFile(filePath string, offset, limit int) (string, int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	lineCount := 0
	scanner := NewLineScanner(file)
	var lines []string
	for scanner.Scan() {
		lineCount++
		if lineCount <= offset || len(lines) >= limit {
			continue
		}
		lineText := scanner.Text()
		if len(lineText) > MaxLineLength {
			characters := []rune(lineText)
			if len(characters) > MaxLineLength {
				lineText = string(characters[:MaxLineLength]) + "..."
			}
		}
		lines = append(lines, lineText)
	}

	if err := scanner.Err(); err != nil {
		return "", 0, err
	}

	return strings.Join(lines, "\n"), lineCount, nil
}

func isImageFile(filePath string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		return true, "JPEG"
	case ".png":
		return true, "PNG"
	case ".gif":
		return true, "GIF"
	case ".bmp":
		return true, "BMP"
	case ".svg":
		return true, "SVG"
	case ".webp":
		return true, "WebP"
	default:
		return false, ""
	}
}

type LineScanner struct {
	scanner *bufio.Scanner
}

func NewLineScanner(r io.Reader) *LineScanner {
	scanner := bufio.NewScanner(r)
	// A permitted file can consist of one long line. Leave room for the scanner
	// to determine EOF even when that line is exactly MaxReadSize bytes long.
	scanner.Buffer(make([]byte, 64*1024), MaxReadSize+1)
	return &LineScanner{
		scanner: scanner,
	}
}

func (s *LineScanner) Scan() bool {
	return s.scanner.Scan()
}

func (s *LineScanner) Text() string {
	return s.scanner.Text()
}

func (s *LineScanner) Err() error {
	return s.scanner.Err()
}
