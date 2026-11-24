package lsp

import (
	"encoding/json"
	"sync"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/lsp/util"
)

// Requests

func HandleWorkspaceConfiguration(params json.RawMessage) (any, error) {
	return []map[string]any{{}}, nil
}

func HandleRegisterCapability(params json.RawMessage) (any, error) {
	return handleRegisterCapability(params, nil)
}

func (c *Client) HandleRegisterCapability(params json.RawMessage) (any, error) {
	return handleRegisterCapability(params, c.notifyFileWatchRegistration)
}

func handleRegisterCapability(params json.RawMessage, notify FileWatchRegistrationHandler) (any, error) {
	var registerParams protocol.RegistrationParams
	if err := json.Unmarshal(params, &registerParams); err != nil {
		logging.Error("Error unmarshaling registration params", "error", err)
		return nil, err
	}

	for _, reg := range registerParams.Registrations {
		switch reg.Method {
		case "workspace/didChangeWatchedFiles":
			// Parse the registration options
			optionsJSON, err := json.Marshal(reg.RegisterOptions)
			if err != nil {
				logging.Error("Error marshaling registration options", "error", err)
				continue
			}

			var options protocol.DidChangeWatchedFilesRegistrationOptions
			if err := json.Unmarshal(optionsJSON, &options); err != nil {
				logging.Error("Error unmarshaling registration options", "error", err)
				continue
			}

			if notify != nil {
				notify(reg.ID, options.Watchers)
			}
		}
	}

	return nil, nil
}

func HandleApplyEdit(params json.RawMessage) (any, error) {
	var edit protocol.ApplyWorkspaceEditParams
	if err := json.Unmarshal(params, &edit); err != nil {
		return nil, err
	}

	paths, err := util.WorkspaceEditPaths(edit.Edit)
	if err != nil {
		return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
	}
	if len(paths) > 0 && !workspaceEditApproved(paths) {
		return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: "workspace edit approval denied"}, nil
	}

	err = util.ApplyWorkspaceEdit(edit.Edit)
	if err != nil {
		logging.Error("Error applying workspace edit", "error", err)
		return protocol.ApplyWorkspaceEditResult{Applied: false, FailureReason: err.Error()}, nil
	}

	return protocol.ApplyWorkspaceEditResult{Applied: true}, nil
}

// WorkspaceEditApproval is an optional application-level approval gate for
// edits requested by an LSP server. The handler remains usable in library
// contexts without a permission service; when registered, approval is checked
// after all paths have passed workspace containment validation and before any
// file is changed.
type WorkspaceEditApproval func(paths []string) bool

var workspaceEditApproval struct {
	sync.RWMutex
	handler WorkspaceEditApproval
}

// SetWorkspaceEditApproval installs the approval gate used by
// workspace/applyEdit. Passing nil removes the gate.
func SetWorkspaceEditApproval(handler WorkspaceEditApproval) {
	workspaceEditApproval.Lock()
	workspaceEditApproval.handler = handler
	workspaceEditApproval.Unlock()
}

func workspaceEditApproved(paths []string) bool {
	workspaceEditApproval.RLock()
	handler := workspaceEditApproval.handler
	workspaceEditApproval.RUnlock()
	// LSP servers are external actors. If the application has not installed an
	// approval gate yet, fail closed instead of allowing a write by default.
	return handler != nil && handler(paths)
}

// FileWatchRegistrationHandler is a function that will be called when file watch registrations are received
type FileWatchRegistrationHandler func(id string, watchers []protocol.FileSystemWatcher)

// RegisterFileWatchHandler installs the callback for this client only. Each
// language server owns independent dynamic registrations.
func (c *Client) RegisterFileWatchHandler(handler FileWatchRegistrationHandler) {
	if c == nil {
		return
	}
	c.fileWatchHandlerMu.Lock()
	c.fileWatchHandler = handler
	c.fileWatchHandlerMu.Unlock()
}

func (c *Client) notifyFileWatchRegistration(id string, watchers []protocol.FileSystemWatcher) {
	if c == nil {
		return
	}
	c.fileWatchHandlerMu.RLock()
	handler := c.fileWatchHandler
	c.fileWatchHandlerMu.RUnlock()
	if handler != nil {
		handler(id, append([]protocol.FileSystemWatcher(nil), watchers...))
	}
}

// Notifications

func HandleServerMessage(params json.RawMessage) {
	cnf := config.Get()
	var msg struct {
		Type    int    `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(params, &msg); err == nil {
		if cnf.DebugLSP {
			logging.Debug("Server message", "type", msg.Type, "message", msg.Message)
		}
	}
}

func HandleDiagnostics(client *Client, params json.RawMessage) {
	var diagParams protocol.PublishDiagnosticsParams
	if err := json.Unmarshal(params, &diagParams); err != nil {
		logging.Error("Error unmarshaling diagnostics params", "error", err)
		return
	}

	client.diagnosticsMu.Lock()
	defer client.diagnosticsMu.Unlock()

	client.diagnostics[diagParams.URI] = diagParams.Diagnostics
}
