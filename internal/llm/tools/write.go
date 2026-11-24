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

type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type WritePermissionsParams struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff"`
}

type writeTool struct {
	lspClients  lsp.ClientProvider
	permissions permission.Service
	files       history.Service
}

type WriteResponseMetadata struct {
	Diff        string `json:"diff"`
	Additions   int    `json:"additions"`
	Removals    int    `json:"removals"`
	ContentHash string `json:"content_hash"`
}

const (
	WriteToolName    = "write"
	writeDescription = `File writing tool that creates or updates files in the filesystem, allowing you to save or modify text content.

WHEN TO USE THIS TOOL:
- Use when you need to create a new file
- Helpful for updating existing files with modified content
- Perfect for saving generated code, configurations, or text data

HOW TO USE:
- Provide the path to the file you want to write
- Include the content to be written to the file
- The tool will create any necessary parent directories

FEATURES:
- Can create new files or overwrite existing ones
- Creates parent directories automatically if they don't exist
- Checks if the file has been modified since last read for safety
- Avoids unnecessary writes when content hasn't changed

LIMITATIONS:
- You should read a file before writing to it to avoid conflicts
- Cannot append to files (rewrites the entire file)


TIPS:
- Use the View tool first to examine existing files before modifying them
- Use the LS tool to verify the correct location when creating new files
- Combine with Glob and Grep tools to find and modify multiple files
- Always include descriptive comments when making changes to existing code`
)

func NewWriteTool(lspClients map[string]*lsp.Client, permissions permission.Service, files history.Service) BaseTool {
	return NewWriteToolWithClientProvider(lsp.NewStaticClientProvider(lspClients), permissions, files)
}

func NewWriteToolWithClientProvider(lspClients lsp.ClientProvider, permissions permission.Service, files history.Service) BaseTool {
	return &writeTool{
		lspClients:  lspClients,
		permissions: permissions,
		files:       files,
	}
}

func (w *writeTool) Info() ToolInfo {
	return ToolInfo{
		Name:        WriteToolName,
		Description: writeDescription,
		Parameters: map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "The path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write to the file",
			},
		},
		Required: []string{"file_path", "content"},
	}
}

func (w *writeTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params WriteParams
	if err := DecodeToolInput(call.Input, &params, "file_path", "content"); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	if params.FilePath == "" {
		return NewTextErrorResponse("file_path is required"), nil
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(config.WorkingDirectory(), filePath)
	}
	resolvedPath, resolveErr := security.ResolveWorkspacePath(config.WorkingDirectory(), filePath)
	if resolveErr != nil {
		return NewTextErrorResponse("file path is outside the workspace: " + resolveErr.Error()), nil
	}
	filePath = resolvedPath

	var oldBytes []byte
	fileExisted := false
	fileInfo, err := os.Stat(filePath)
	if err == nil {
		fileExisted = true
		if fileInfo.IsDir() {
			return NewTextErrorResponse(fmt.Sprintf("Path is a directory, not a file: %s", filePath)), nil
		}

		modTime := fileInfo.ModTime()
		lastRead := getLastReadTime(ctx, filePath)
		if modTime.After(lastRead) {
			return NewTextErrorResponse(fmt.Sprintf("File %s has been modified since it was last read.\nLast modification: %s\nLast read: %s\n\nPlease read the file again before modifying it.",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339))), nil
		}

		oldBytes, err = os.ReadFile(filePath)
		if err != nil {
			return NewTextErrorResponseWithError(fmt.Errorf("error reading existing file: %w", err))
		}
		if string(oldBytes) == params.Content {
			return NewTextErrorResponse(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath)), nil
		}
	} else if !os.IsNotExist(err) {
		return NewTextErrorResponseWithError(fmt.Errorf("error checking file: %w", err))
	}

	oldContent := string(oldBytes)

	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewTextErrorResponseWithError(fmt.Errorf("session_id and message_id are required"))
	}

	diff, additions, removals := diff.GenerateDiff(
		oldContent,
		params.Content,
		filePath,
	)

	rootDir := config.WorkingDirectory()
	permissionPath := filepath.Dir(filePath)
	if strings.HasPrefix(filePath, rootDir) {
		permissionPath = rootDir
	}
	if w.permissions == nil {
		return NewTextErrorResponseWithError(fmt.Errorf("permission service is unavailable"))
	}
	p := w.permissions.RequestContext(ctx,
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        permissionPath,
			ScopeKind:   permission.ScopeDirectory,
			ToolName:    WriteToolName,
			Action:      "write",
			Description: fmt.Sprintf("Create file %s", filePath),
			Params: WritePermissionsParams{
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

	// Approval covers the exact diff shown to the user. Revalidate the target
	// after the potentially long prompt before making any filesystem mutation.
	if err := verifyFilePathAfterApproval(filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}
	if fileExisted {
		if err := verifyFileSnapshot(filePath, oldBytes); err != nil {
			return NewTextErrorResponse(err.Error()), nil
		}
	} else if err := verifyFileStillAbsent(filePath); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	dir := filepath.Dir(filePath)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("error creating directory: %w", err))
	}

	err = os.WriteFile(filePath, []byte(params.Content), 0o644)
	if err != nil {
		return NewTextErrorResponseWithError(fmt.Errorf("error writing file: %w", err))
	}

	// History is bookkeeping after the committed filesystem write. A history
	// outage must not turn a successful mutation into a false failure that the
	// model may retry.
	historyWarning := ""
	if w.files != nil {
		file, historyErr := w.files.GetByPathAndSession(ctx, filePath, sessionID)
		if historyErr != nil {
			if _, createErr := w.files.Create(ctx, sessionID, filePath, oldContent); createErr != nil {
				historyWarning = fmt.Sprintf("file was written, but history initialization failed: %v", createErr)
				logging.Warn("file history initialization failed after write", "path", filePath, "error", createErr)
			}
		} else if file.Content != oldContent {
			if _, versionErr := w.files.CreateVersion(ctx, sessionID, filePath, oldContent); versionErr != nil {
				historyWarning = fmt.Sprintf("file was written, but preserving its prior history failed: %v", versionErr)
				logging.Debug("Error creating file history version", "error", versionErr)
			}
		}
		if _, versionErr := w.files.CreateVersion(ctx, sessionID, filePath, params.Content); versionErr != nil {
			if historyWarning == "" {
				historyWarning = fmt.Sprintf("file was written, but recording its new history version failed: %v", versionErr)
			}
			logging.Debug("Error creating file history version", "error", versionErr)
		}
	}

	recordFileWrite(ctx, filePath)
	recordFileRead(ctx, filePath)
	lsps := w.lspClients.Snapshot()
	waitForLspDiagnostics(ctx, filePath, lsps)

	result := fmt.Sprintf("File successfully written: %s", filePath)
	if historyWarning != "" {
		result += "\nWarning: " + historyWarning
	}
	result = fmt.Sprintf("<result>\n%s\n</result>", result)
	result += getDiagnostics(filePath, lsps)
	return WithResponseMetadata(NewTextResponse(result),
		WriteResponseMetadata{
			Diff:        diff,
			Additions:   additions,
			Removals:    removals,
			ContentHash: contentSHA256([]byte(params.Content)),
		},
	), nil
}
