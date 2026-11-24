package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/permission"
)

type PatchParams struct {
	PatchText string `json:"patch_text"`
}

type PatchResponseMetadata struct {
	FilesChanged []string `json:"files_changed"`
	Additions    int      `json:"additions"`
	Removals     int      `json:"removals"`
}

type patchTool struct {
	lspClients  lsp.ClientProvider
	permissions permission.Service
	files       history.Service
}

const (
	PatchToolName    = "patch"
	patchDescription = `Applies a patch to multiple files in one operation. This tool is useful for making coordinated changes across multiple files.

The patch text must follow this format:
*** Begin Patch
*** Update File: /path/to/file
*** Move to: /path/to/destination
@@ Context line (unique within the file)
 Line to keep
-Line to remove
+Line to add
 Line to keep
*** Add File: /path/to/new/file
+Content of the new file
+More content
*** Delete File: /path/to/file/to/delete
*** End Patch

Before using this tool:
1. Use the FileRead tool to understand the files' contents and context
2. Verify all file paths are correct (use the LS tool)

CRITICAL REQUIREMENTS FOR USING THIS TOOL:

1. UNIQUENESS: Context lines MUST uniquely identify the specific sections you want to change
2. PRECISION: All whitespace, indentation, and surrounding code must match exactly
3. VALIDATION: Ensure edits result in idiomatic, correct code
4. PATHS: Always use absolute file paths (starting with /)

The tool will apply all changes in a single atomic operation.`
)

func NewPatchTool(lspClients map[string]*lsp.Client, permissions permission.Service, files history.Service) BaseTool {
	return NewPatchToolWithClientProvider(lsp.NewStaticClientProvider(lspClients), permissions, files)
}

func NewPatchToolWithClientProvider(lspClients lsp.ClientProvider, permissions permission.Service, files history.Service) BaseTool {
	return &patchTool{
		lspClients:  lspClients,
		permissions: permissions,
		files:       files,
	}
}

func (p *patchTool) Info() ToolInfo {
	return ToolInfo{
		Name:        PatchToolName,
		Description: patchDescription,
		Parameters: map[string]any{
			"patch_text": map[string]any{
				"type":        "string",
				"description": "The full patch text that describes all changes to be made",
			},
		},
		Required: []string{"patch_text"},
	}
}

func resolvePatchCommitPaths(commit *diff.Commit) error {
	resolvedChanges := make(map[string]diff.FileChange, len(commit.Changes))
	for path, change := range commit.Changes {
		resolvedPath, err := resolveWorkspaceToolPath(path)
		if err != nil {
			return err
		}

		if change.MovePath != nil {
			resolvedMovePath, err := resolveWorkspaceToolPath(*change.MovePath)
			if err != nil {
				return err
			}
			change.MovePath = &resolvedMovePath
		}

		if _, exists := resolvedChanges[resolvedPath]; exists {
			return fmt.Errorf("multiple patch paths resolve to %s", resolvedPath)
		}
		resolvedChanges[resolvedPath] = change
	}
	commit.Changes = resolvedChanges
	return nil
}

func (p *patchTool) Run(ctx context.Context, call ToolCall) (ToolResponse, error) {
	var params PatchParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return NewTextErrorResponse("invalid parameters"), nil
	}

	if params.PatchText == "" {
		return NewTextErrorResponse("patch_text is required"), nil
	}

	// Identify all files needed for the patch and verify they've been read
	filesToRead := diff.IdentifyFilesNeeded(params.PatchText)
	for _, filePath := range filesToRead {
		absPath, resolveErr := resolveWorkspaceToolPath(filePath)
		if resolveErr != nil {
			return NewTextErrorResponse("file path is outside the workspace: " + resolveErr.Error()), nil
		}

		if getLastReadTime(ctx, absPath).IsZero() {
			return NewTextErrorResponse(fmt.Sprintf("you must read the file %s before patching it. Use the FileRead tool first", filePath)), nil
		}

		fileInfo, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				return NewTextErrorResponse(fmt.Sprintf("file not found: %s", absPath)), nil
			}
			return ToolResponse{}, fmt.Errorf("failed to access file: %w", err)
		}

		if fileInfo.IsDir() {
			return NewTextErrorResponse(fmt.Sprintf("path is a directory, not a file: %s", absPath)), nil
		}

		modTime := fileInfo.ModTime()
		lastRead := getLastReadTime(ctx, absPath)
		if modTime.After(lastRead) {
			return NewTextErrorResponse(
				fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
					absPath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339),
				)), nil
		}
	}

	// Check for new files to ensure they don't already exist
	filesToAdd := diff.IdentifyFilesAdded(params.PatchText)
	for _, filePath := range filesToAdd {
		absPath, resolveErr := resolveWorkspaceToolPath(filePath)
		if resolveErr != nil {
			return NewTextErrorResponse("file path is outside the workspace: " + resolveErr.Error()), nil
		}

		_, err := os.Stat(absPath)
		if err == nil {
			return NewTextErrorResponse(fmt.Sprintf("file already exists and cannot be added: %s", absPath)), nil
		} else if !os.IsNotExist(err) {
			return ToolResponse{}, fmt.Errorf("failed to check file: %w", err)
		}
	}

	// Load all required files
	currentFiles := make(map[string]string)
	for _, filePath := range filesToRead {
		absPath, resolveErr := resolveWorkspaceToolPath(filePath)
		if resolveErr != nil {
			return NewTextErrorResponse("file path is outside the workspace: " + resolveErr.Error()), nil
		}

		content, err := os.ReadFile(absPath)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("failed to read file %s: %w", absPath, err)
		}
		currentFiles[filePath] = string(content)
	}

	// Process the patch
	patch, fuzz, err := diff.TextToPatch(params.PatchText, currentFiles)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to parse patch: %s", err)), nil
	}

	if fuzz > 3 {
		return NewTextErrorResponse(fmt.Sprintf("patch contains fuzzy matches (fuzz level: %d). Please make your context lines more precise", fuzz)), nil
	}

	// Convert patch to commit
	commit, err := diff.PatchToCommit(patch, currentFiles)
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to create commit from patch: %s", err)), nil
	}
	if err := resolvePatchCommitPaths(&commit); err != nil {
		return NewTextErrorResponse("patch path is outside the workspace: " + err.Error()), nil
	}
	for sourcePath, change := range commit.Changes {
		if change.MovePath == nil {
			continue
		}
		if sourcePath == *change.MovePath {
			return NewTextErrorResponse(fmt.Sprintf("move source and destination are the same file: %s", sourcePath)), nil
		}
		if _, statErr := os.Lstat(*change.MovePath); statErr == nil {
			return NewTextErrorResponse(fmt.Sprintf("move destination already exists: %s", *change.MovePath)), nil
		} else if !os.IsNotExist(statErr) {
			return NewTextErrorResponseWithError(fmt.Errorf("failed to check move destination %s: %w", *change.MovePath, statErr))
		}
	}

	// Get session ID and message ID
	sessionID, messageID := GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return NewTextErrorResponseWithError(fmt.Errorf("session ID and message ID are required for creating a patch"))
	}
	if p.permissions == nil {
		return NewTextErrorResponseWithError(fmt.Errorf("permission service is unavailable"))
	}
	if p.files == nil {
		return NewTextErrorResponseWithError(fmt.Errorf("file history service is unavailable"))
	}

	// Request permission for all changes
	for path, change := range commit.Changes {
		switch change.Type {
		case diff.ActionAdd:
			dir := filepath.Dir(path)
			patchDiff, _, _ := diff.GenerateDiff("", *change.NewContent, path)
			p := p.permissions.RequestContext(ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        dir,
					ScopeKind:   permission.ScopeDirectory,
					ToolName:    PatchToolName,
					Action:      "create",
					Description: fmt.Sprintf("Create file %s", path),
					Params: EditPermissionsParams{
						FilePath: path,
						Diff:     patchDiff,
					},
				},
			)
			if !p {
				return ToolResponse{}, permission.ErrorPermissionDenied
			}
		case diff.ActionUpdate:
			currentContent := ""
			if change.OldContent != nil {
				currentContent = *change.OldContent
			}
			newContent := ""
			if change.NewContent != nil {
				newContent = *change.NewContent
			}
			if change.MovePath != nil {
				sourceDiff, _, _ := diff.GenerateDiff(currentContent, "", path)
				if !p.requestPermission(ctx, sessionID, path, "delete", fmt.Sprintf("Move file %s to %s (remove source)", path, *change.MovePath), sourceDiff) {
					return ToolResponse{}, permission.ErrorPermissionDenied
				}
				targetDiff, _, _ := diff.GenerateDiff("", newContent, *change.MovePath)
				if !p.requestPermission(ctx, sessionID, *change.MovePath, "create", fmt.Sprintf("Move file %s to %s (create destination)", path, *change.MovePath), targetDiff) {
					return ToolResponse{}, permission.ErrorPermissionDenied
				}
			} else {
				patchDiff, _, _ := diff.GenerateDiff(currentContent, newContent, path)
				if !p.requestPermission(ctx, sessionID, path, "update", fmt.Sprintf("Update file %s", path), patchDiff) {
					return ToolResponse{}, permission.ErrorPermissionDenied
				}
			}
		case diff.ActionDelete:
			dir := filepath.Dir(path)
			patchDiff, _, _ := diff.GenerateDiff(*change.OldContent, "", path)
			p := p.permissions.RequestContext(ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        dir,
					ScopeKind:   permission.ScopeDirectory,
					ToolName:    PatchToolName,
					Action:      "delete",
					Description: fmt.Sprintf("Delete file %s", path),
					Params: EditPermissionsParams{
						FilePath: path,
						Diff:     patchDiff,
					},
				},
			)
			if !p {
				return ToolResponse{}, permission.ErrorPermissionDenied
			}
		}
	}

	// Approval covers the snapshots used to generate the diffs. Validate every
	// source and destination before applying any part of the patch, since an
	// approval prompt can remain open while another process edits these files.
	for path, change := range commit.Changes {
		if err := ctx.Err(); err != nil {
			return NewTextErrorResponseWithError(err)
		}
		if err := verifyFilePathAfterApproval(path); err != nil {
			return NewTextErrorResponse(err.Error()), nil
		}
		if change.Type == diff.ActionAdd {
			if err := verifyFileStillAbsent(path); err != nil {
				return NewTextErrorResponse(err.Error()), nil
			}
		} else if change.OldContent != nil {
			if err := verifyFileSnapshot(path, []byte(*change.OldContent)); err != nil {
				return NewTextErrorResponse(err.Error()), nil
			}
		}
		if change.MovePath != nil {
			if err := verifyFilePathAfterApproval(*change.MovePath); err != nil {
				return NewTextErrorResponse(err.Error()), nil
			}
			if err := verifyFileStillAbsent(*change.MovePath); err != nil {
				return NewTextErrorResponse(err.Error()), nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return NewTextErrorResponseWithError(err)
	}

	// Apply the changes to the filesystem
	err = diff.ApplyCommit(commit, func(path string, content string) error {
		absPath, resolveErr := resolveWorkspaceToolPath(path)
		if resolveErr != nil {
			return fmt.Errorf("path is outside the workspace: %w", resolveErr)
		}

		// Create parent directories if needed
		dir := filepath.Dir(absPath)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create parent directories for %s: %w", absPath, err)
		}

		return os.WriteFile(absPath, []byte(content), 0o644)
	}, func(path string) error {
		absPath, resolveErr := resolveWorkspaceToolPath(path)
		if resolveErr != nil {
			return fmt.Errorf("path is outside the workspace: %w", resolveErr)
		}
		return os.Remove(absPath)
	})
	if err != nil {
		return NewTextErrorResponse(fmt.Sprintf("failed to apply patch: %s", err)), nil
	}

	// Update file history for all modified files
	changedFiles := []string{}
	totalAdditions := 0
	totalRemovals := 0

	for path, change := range commit.Changes {
		absPath, resolveErr := resolveWorkspaceToolPath(path)
		if resolveErr != nil {
			return ToolResponse{}, fmt.Errorf("path is outside the workspace: %w", resolveErr)
		}
		oldContent := ""
		if change.OldContent != nil {
			oldContent = *change.OldContent
		}

		newContent := ""
		if change.NewContent != nil {
			newContent = *change.NewContent
		}

		// Calculate diff statistics
		_, additions, removals := diff.GenerateDiff(oldContent, newContent, path)
		totalAdditions += additions
		totalRemovals += removals

		if change.MovePath != nil {
			targetPath := *change.MovePath
			changedFiles = append(changedFiles, absPath, targetPath)
			p.recordHistory(ctx, sessionID, absPath, diff.ActionDelete, oldContent, "")
			p.recordHistory(ctx, sessionID, targetPath, diff.ActionAdd, "", newContent)
			recordFileWrite(ctx, absPath)
			recordFileRead(ctx, absPath)
			recordFileWrite(ctx, targetPath)
			recordFileRead(ctx, targetPath)
		} else {
			changedFiles = append(changedFiles, absPath)
			p.recordHistory(ctx, sessionID, absPath, change.Type, oldContent, newContent)
			recordFileWrite(ctx, absPath)
			recordFileRead(ctx, absPath)
		}
	}

	// Run LSP diagnostics on all changed files
	lsps := p.lspClients.Snapshot()
	for _, filePath := range changedFiles {
		waitForLspDiagnostics(ctx, filePath, lsps)
	}

	result := fmt.Sprintf("Patch applied successfully. %d files changed, %d additions, %d removals",
		len(changedFiles), totalAdditions, totalRemovals)

	diagnosticsText := ""
	for _, filePath := range changedFiles {
		diagnosticsText += getDiagnostics(filePath, lsps)
	}

	if diagnosticsText != "" {
		result += "\n\nDiagnostics:\n" + diagnosticsText
	}

	return WithResponseMetadata(
		NewTextResponse(result),
		PatchResponseMetadata{
			FilesChanged: changedFiles,
			Additions:    totalAdditions,
			Removals:     totalRemovals,
		}), nil
}

func (p *patchTool) requestPermission(ctx context.Context, sessionID, path, action, description, patchDiff string) bool {
	return p.permissions.RequestContext(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        filepath.Dir(path),
		ScopeKind:   permission.ScopeDirectory,
		ToolName:    PatchToolName,
		Action:      action,
		Description: description,
		Params: EditPermissionsParams{
			FilePath: path,
			Diff:     patchDiff,
		},
	})
}

func (p *patchTool) recordHistory(ctx context.Context, sessionID, path string, action diff.ActionType, oldContent, newContent string) {
	file, err := p.files.GetByPathAndSession(ctx, path, sessionID)
	if err != nil && action != diff.ActionAdd {
		if _, createErr := p.files.Create(ctx, sessionID, path, oldContent); createErr != nil {
			logging.Debug("Error creating file history", "error", createErr)
		}
	}
	if err == nil && action != diff.ActionAdd && file.Content != oldContent {
		if _, versionErr := p.files.CreateVersion(ctx, sessionID, path, oldContent); versionErr != nil {
			logging.Debug("Error creating file history version", "error", versionErr)
		}
	}
	if action == diff.ActionDelete {
		newContent = ""
	}
	if _, versionErr := p.files.CreateVersion(ctx, sessionID, path, newContent); versionErr != nil {
		logging.Debug("Error creating file history version", "error", versionErr)
	}
}
