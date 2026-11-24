package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/security"
)

type EditParams struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type EditPermissionsParams struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff"`
}

type EditResponseMetadata struct {
	Diff        string `json:"diff"`
	Additions   int    `json:"additions"`
	Removals    int    `json:"removals"`
	ContentHash string `json:"content_hash"`
}

type editTool struct {
	lspClients  lsp.ClientProvider
	permissions permission.Service
	files       history.Service
}

const (
	EditToolName    = "edit"
	editDescription = `Edits files by replacing text, creating new files, or deleting content. For moving or renaming files, use the Bash tool with the 'mv' command instead. For larger file edits, use the FileWrite tool to overwrite files.

Before using this tool:

1. Use the FileRead tool to understand the file's contents and context

2. Verify the directory path is correct (only applicable when creating new files):
   - Use the LS tool to verify the parent directory exists and is the correct location

To make a file edit, provide the following:
1. file_path: The absolute path to the file to modify (must be absolute, not relative)
2. old_string: The text to replace (must be unique within the file, and must match the file contents exactly, including all whitespace and indentation)
3. new_string: The edited text to replace the old_string

Special cases:
- To create a new file: provide file_path and new_string, leave old_string empty
- To delete content: provide file_path and old_string, leave new_string empty

The tool will replace ONE occurrence of old_string with new_string in the specified file.

CRITICAL REQUIREMENTS FOR USING THIS TOOL:

1. UNIQUENESS: The old_string MUST uniquely identify the specific instance you want to change. This means:
   - Include AT LEAST 3-5 lines of context BEFORE the change point
   - Include AT LEAST 3-5 lines of context AFTER the change point
   - Include all whitespace, indentation, and surrounding code exactly as it appears in the file

2. SINGLE INSTANCE: This tool can only change ONE instance at a time. If you need to change multiple instances:
   - Make separate calls to this tool for each instance
   - Each call must uniquely identify its specific instance using extensive context

3. VERIFICATION: Before using this tool:
   - Check how many instances of the target text exist in the file
   - If multiple instances exist, gather enough context to uniquely identify each one
   - Plan separate tool calls for each instance

WARNING: If you do not follow these requirements:
   - The tool will fail if old_string matches multiple locations
   - The tool will fail if old_string doesn't match exactly (including whitespace)
   - You may change the wrong instance if you don't include enough context

When making edits:
   - Ensure the edit results in idiomatic, correct code
   - Do not leave the code in a broken state
   - Always use absolute file paths (starting with /)

Remember: when making multiple file edits in a row to the same file, you should prefer to send all edits in a single message with multiple calls to this tool, rather than multiple messages with a single call each.`
)

func NewEditTool(lspClients map[string]*lsp.Client, permissions permission.Service, files history.Service) BaseTool {
	return NewEditToolWithClientProvider(lsp.NewStaticClientProvider(lspClients), permissions, files)
}

func NewEditToolWithClientProvider(lspClients lsp.ClientProvider, permissions permission.Service, files history.Service) BaseTool {
	return &editTool{
		lspClients:  lspClients,
		permissions: permissions,
		files:       files,
	}
}

func (e *editTool) Info() ToolInfo {
	return ToolInfo{
		Name:        EditToolName,
		Description: editDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The absolute path to the file to modify",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "The text to replace",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "The text to replace it with",
			},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
}

func (e *editTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params EditParams
	if err := DecodeToolInput(call.Input, &params, "file_path"); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	if !filepath.IsAbs(params.FilePath) {
		wd := config.WorkingDirectory()
		params.FilePath = filepath.Join(wd, params.FilePath)
	}
	resolvedPath, resolveErr := security.ResolveWorkspacePath(config.WorkingDirectory(), params.FilePath)
	if resolveErr != nil {
		return NewTextErrorResponse("file path is outside the workspace: " + resolveErr.Error()), nil
	}
	params.FilePath = resolvedPath

	var response ToolResponse
	var err error

	if params.OldString == "" {
		return e.createNewFile(ctx, params.FilePath, params.NewString)
	}

	if params.NewString == "" {
		return e.deleteContent(ctx, params.FilePath, params.OldString)
	}

	response, err = e.replaceContent(ctx, params.FilePath, params.OldString, params.NewString)
	if err != nil {
		return response, err
	}
	if response.IsError {
		// Return early if there was an error during content replacement
		// This prevents unnecessary LSP diagnostics processing
		return response, nil
	}

	lsps := e.lspClients.Snapshot()
	waitForLspDiagnostics(ctx, params.FilePath, lsps)
	text := fmt.Sprintf("<result>\n%s\n</result>\n", response.Content)
	text += getDiagnostics(params.FilePath, lsps)
	response.Content = text
	return response, nil
}

func (e *editTool) createNewFile(ctx context.Context, filePath, content string) (ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err == nil {
		if fileInfo.IsDir() {
			return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("file already exists: %s", filePath)), nil
	} else if !os.IsNotExist(err) {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to access file: %w", err))
	}

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewTextErrorResponseWithError(fmt.Errorf("session ID and message ID are required for creating a new file"))
	}

	diff, additions, removals := diff.GenerateDiff(
		"",
		content,
		filePath,
	)
	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	if e.permissions == nil {
		return NewTextErrorResponseWithError(fmt.Errorf("permission service is unavailable"))
	}
	p := e.permissions.RequestContext(ctx,
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ScopeKind:   permission.ScopeDirectory,
			ToolName:    EditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Create file %s", filePath),
			Params: EditPermissionsParams{
				FilePath: filePath,
				Diff:     diff,
			},
		},
	)
	if !p {
		return NewTextErrorResponse("permission denied"), permission.ErrorPermissionDenied
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}
	if err := verifyFilePathAfterApproval(filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if err := verifyFileStillAbsent(filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	dir := filepath.Dir(filePath)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to create parent directories: %w", err))
	}

	err = os.WriteFile(filePath, []byte(content), 0o644)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to write file: %w", err))
	}

	// File can't be in the history so we create a new file history
	historyWarning := ""
	if e.files != nil {
		if _, historyErr := e.files.Create(ctx, sessionID, filePath, ""); historyErr != nil {
			historyWarning = fmt.Sprintf("file was created, but history initialization failed: %v", historyErr)
			logging.Warn("file history initialization failed after create", "path", filePath, "error", historyErr)
		}

		// Add the new content to the file history.
		if _, historyErr := e.files.CreateVersion(ctx, sessionID, filePath, content); historyErr != nil {
			if historyWarning == "" {
				historyWarning = fmt.Sprintf("file was created, but recording its history version failed: %v", historyErr)
			}
			logging.Debug("Error creating file history version", "error", historyErr)
		}
	}

	recordFileWrite(ctx, filePath)
	recordFileRead(ctx, filePath)

	result := "File created: " + filePath
	if historyWarning != "" {
		result += "\nWarning: " + historyWarning
	}
	return WithResponseMetadata(
		NewTextResponse(result),
		EditResponseMetadata{
			Diff:        diff,
			Additions:   additions,
			Removals:    removals,
			ContentHash: contentSHA256([]byte(content)),
		},
	), nil
}

func (e *editTool) deleteContent(ctx context.Context, filePath, oldString string) (ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return NewTextErrorResponseWithError(fmt.Errorf("failed to access file: %w", err))
	}

	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	if getLastReadTime(ctx, filePath).IsZero() {
		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
	}

	modTime := fileInfo.ModTime()
	lastRead := getLastReadTime(ctx, filePath)
	if modTime.After(lastRead) {
		return NewTextErrorResponse(
			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
			)), nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to read file: %w", err))
	}

	oldContent := string(content)

	index := strings.Index(oldContent, oldString)
	if index == -1 {
		return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
	}

	lastIndex := strings.LastIndex(oldContent, oldString)
	if index != lastIndex {
		return NewTextErrorResponse("old_string appears multiple times in the file. Please provide more context to ensure a unique match"), nil
	}

	newContent := oldContent[:index] + oldContent[index+len(oldString):]

	sessionID, messageID := GetContextValues(ctx)

	if sessionID == "" || messageID == "" {
		return NewTextErrorResponseWithError(fmt.Errorf("session ID and message ID are required for editing a file"))
	}

	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		filePath,
	)

	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	if e.permissions == nil {
		return NewTextErrorResponseWithError(fmt.Errorf("permission service is unavailable"))
	}
	p := e.permissions.RequestContext(ctx,
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ScopeKind:   permission.ScopeDirectory,
			ToolName:    EditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Delete content from file %s", filePath),
			Params: EditPermissionsParams{
				FilePath: filePath,
				Diff:     diff,
			},
		},
	)
	if !p {
		return NewTextErrorResponse("permission denied"), permission.ErrorPermissionDenied
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}
	if err := verifyFilePathAfterApproval(filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if err := verifyFileSnapshot(filePath, content); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	err = os.WriteFile(filePath, []byte(newContent), 0o644)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to write file: %w", err))
	}

	// Check if file exists in history
	historyWarning := ""
	if e.files != nil {
		file, historyErr := e.files.GetByPathAndSession(ctx, filePath, sessionID)
		if historyErr != nil {
			if _, createErr := e.files.Create(ctx, sessionID, filePath, oldContent); createErr != nil {
				historyWarning = fmt.Sprintf("content was deleted, but history initialization failed: %v", createErr)
				logging.Warn("file history initialization failed after edit", "path", filePath, "error", createErr)
			}
		} else if file.Content != oldContent {
			if _, versionErr := e.files.CreateVersion(ctx, sessionID, filePath, oldContent); versionErr != nil {
				historyWarning = fmt.Sprintf("content was deleted, but preserving its prior history failed: %v", versionErr)
				logging.Debug("Error creating file history version", "error", versionErr)
			}
		}
		if _, versionErr := e.files.CreateVersion(ctx, sessionID, filePath, newContent); versionErr != nil {
			if historyWarning == "" {
				historyWarning = fmt.Sprintf("content was deleted, but recording its new history version failed: %v", versionErr)
			}
			logging.Debug("Error creating file history version", "error", versionErr)
		}
	}

	recordFileWrite(ctx, filePath)
	recordFileRead(ctx, filePath)

	result := "Content deleted from file: " + filePath
	if historyWarning != "" {
		result += "\nWarning: " + historyWarning
	}
	return WithResponseMetadata(
		NewTextResponse(result),
		EditResponseMetadata{
			Diff:        diff,
			Additions:   additions,
			Removals:    removals,
			ContentHash: contentSHA256([]byte(newContent)),
		},
	), nil
}

func (e *editTool) replaceContent(ctx context.Context, filePath, oldString, newString string) (ToolResponse, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return NewTextErrorResponseWithError(fmt.Errorf("failed to access file: %w", err))
	}

	if fileInfo.IsDir() {
		return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", filePath)), nil
	}

	if getLastReadTime(ctx, filePath).IsZero() {
		return NewTextErrorResponse("you must read the file before editing it. Use the View tool first"), nil
	}

	modTime := fileInfo.ModTime()
	lastRead := getLastReadTime(ctx, filePath)
	if modTime.After(lastRead) {
		return NewTextErrorResponse(
			fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
			)), nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to read file: %w", err))
	}

	oldContent := string(content)

	index := strings.Index(oldContent, oldString)
	if index == -1 {
		return NewTextErrorResponse("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks"), nil
	}

	lastIndex := strings.LastIndex(oldContent, oldString)
	if index != lastIndex {
		return NewTextErrorResponse("old_string appears multiple times in the file. Please provide more context to ensure a unique match"), nil
	}

	newContent := oldContent[:index] + newString + oldContent[index+len(oldString):]

	if oldContent == newContent {
		return NewTextErrorResponse("new content is the same as old content. No changes made."), nil
	}
	sessionID, messageID := GetContextValues(ctx)

	if sessionID == "" || messageID == "" {
		return NewTextErrorResponseWithError(fmt.Errorf("session ID and message ID are required for editing a file"))
	}
	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		newContent,
		filePath,
	)
	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	if e.permissions == nil {
		return NewTextErrorResponseWithError(fmt.Errorf("permission service is unavailable"))
	}
	p := e.permissions.RequestContext(ctx,
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ScopeKind:   permission.ScopeDirectory,
			ToolName:    EditToolName,
			Action:      "write",
			Description: fmt.Sprintf("Replace content in file %s", filePath),
			Params: EditPermissionsParams{
				FilePath: filePath,
				Diff:     diff,
			},
		},
	)
	if !p {
		return NewTextErrorResponse("permission denied"), permission.ErrorPermissionDenied
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}
	if err := verifyFilePathAfterApproval(filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if err := verifyFileSnapshot(filePath, content); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	err = os.WriteFile(filePath, []byte(newContent), 0o644)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("failed to write file: %w", err))
	}

	// Check if file exists in history
	historyWarning := ""
	if e.files != nil {
		file, historyErr := e.files.GetByPathAndSession(ctx, filePath, sessionID)
		if historyErr != nil {
			if _, createErr := e.files.Create(ctx, sessionID, filePath, oldContent); createErr != nil {
				historyWarning = fmt.Sprintf("content was replaced, but history initialization failed: %v", createErr)
				logging.Warn("file history initialization failed after edit", "path", filePath, "error", createErr)
			}
		} else if file.Content != oldContent {
			if _, versionErr := e.files.CreateVersion(ctx, sessionID, filePath, oldContent); versionErr != nil {
				historyWarning = fmt.Sprintf("content was replaced, but preserving its prior history failed: %v", versionErr)
				logging.Debug("Error creating file history version", "error", versionErr)
			}
		}
		if _, versionErr := e.files.CreateVersion(ctx, sessionID, filePath, newContent); versionErr != nil {
			if historyWarning == "" {
				historyWarning = fmt.Sprintf("content was replaced, but recording its new history version failed: %v", versionErr)
			}
			logging.Debug("Error creating file history version", "error", versionErr)
		}
	}

	recordFileWrite(ctx, filePath)
	recordFileRead(ctx, filePath)

	result := "Content replaced in file: " + filePath
	if historyWarning != "" {
		result += "\nWarning: " + historyWarning
	}
	return WithResponseMetadata(
		NewTextResponse(result),
		EditResponseMetadata{
			Diff:        diff,
			Additions:   additions,
			Removals:    removals,
			ContentHash: contentSHA256([]byte(newContent)),
		}), nil
}
