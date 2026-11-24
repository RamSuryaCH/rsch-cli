package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/lsp/watcher"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/sandbox"
)

var (
	errUntrustedLSPWorkspace = errors.New("LSP startup requires a trusted workspace")
	errLSPIsolationRequired  = errors.New("LSP isolation is required but unavailable")
)

func (app *App) initLSPClients(ctx context.Context) {
	cfg := config.Get()
	if cfg == nil || !cfg.WorkspaceTrusted {
		logging.Warn("Skipping LSP startup until the workspace is explicitly trusted")
		return
	}
	if app.Permissions != nil {
		lsp.SetWorkspaceEditApproval(func(paths []string) bool {
			return app.Permissions.RequestContext(ctx, permission.CreatePermissionRequest{
				SessionID:   "lsp",
				ToolName:    "lsp",
				Description: "LSP server requested workspace file changes",
				Action:      "write",
				Path:        config.WorkingDirectory(),
				ScopeKind:   permission.ScopeDirectory,
				Params:      map[string]any{"paths": paths},
			})
		})
	}

	// Initialize LSP clients in parallel, but do not return until every startup
	// attempt has either registered its client or cleaned it up.
	var initWG sync.WaitGroup
	for name, clientConfig := range cfg.LSP {
		if !lspClientEnabled(clientConfig) {
			logging.Info("Skipping disabled LSP client", "name", name)
			continue
		}
		initWG.Add(1)
		go func() {
			defer initWG.Done()
			app.createAndStartLSPClient(ctx, name, clientConfig.Command, clientConfig.Args...)
		}()
	}
	initWG.Wait()
	logging.Info("LSP clients initialization started in background")
}

// createAndStartLSPClient creates a new LSP client, initializes it, and starts its workspace watcher
func (app *App) createAndStartLSPClient(ctx context.Context, name string, command string, args ...string) {
	// Create a specific context for initialization with a timeout
	logging.Info("Creating LSP client", "name", name, "command", command, "args", args)

	startOptions, err := lspClientStartOptions(config.Get(), sandbox.Detect(), config.WorkingDirectory(), command, args...)
	if err != nil {
		logging.Error("Refusing to start LSP client", "name", name, "error", err)
		return
	}

	// Create the LSP client through the sanitized process boundary and, when
	// available, the platform sandbox wrapper.
	lspClient, err := lsp.NewClientWithOptions(ctx, startOptions)
	if err != nil {
		logging.Error("Failed to create LSP client for", name, err)
		return
	}
	watchCtx, cancelFunc := context.WithCancel(ctx)
	watchCtx = context.WithValue(watchCtx, "serverName", name)
	workspaceWatcher := watcher.NewWorkspaceWatcherForWorkspace(lspClient, config.WorkingDirectory())
	lspClient.RegisterFileWatchHandler(func(id string, watchers []protocol.FileSystemWatcher) {
		workspaceWatcher.AddRegistrations(watchCtx, id, watchers)
	})

	// Create a longer timeout for initialization (some servers take time to start)
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Initialize with the initialization context
	_, err = lspClient.InitializeLSPClient(initCtx, config.WorkingDirectory())
	if err != nil {
		logging.Error("Initialize failed", "name", name, "error", err)
		// Clean up the client to prevent resource leaks
		cancelFunc()
		_ = lspClient.Close()
		return
	}

	// Wait for the server to be ready
	if err := lspClient.WaitForServerReady(initCtx); err != nil {
		logging.Error("Server failed to become ready", "name", name, "error", err)
		// We'll continue anyway, as some functionality might still work
		lspClient.SetServerState(lsp.StateError)
	} else {
		logging.Info("LSP server is ready", "name", name)
		lspClient.SetServerState(lsp.StateReady)
	}

	logging.Info("LSP client initialized", "name", name)

	// Store the cancel function to be called during cleanup
	app.cancelFuncsMutex.Lock()
	app.watcherCancelFuncs = append(app.watcherCancelFuncs, cancelFunc)
	app.cancelFuncsMutex.Unlock()

	// Add the watcher to a WaitGroup to track active goroutines
	app.watcherWG.Add(1)

	app.LSPClients.Set(name, lspClient)

	go app.runWorkspaceWatcher(watchCtx, name, workspaceWatcher)
}

func lspClientStartOptions(cfg *config.Config, capability sandbox.Capability, workspace, command string, args ...string) (lsp.ClientStartOptions, error) {
	if cfg == nil || !cfg.WorkspaceTrusted {
		return lsp.ClientStartOptions{}, errUntrustedLSPWorkspace
	}
	argv := append([]string{command}, args...)
	if capability.Isolated {
		runner := sandbox.PlatformRunner{
			Capability:       capability,
			WorkingDirectory: workspace,
			Mode:             sandbox.ReadOnly,
			AllowNetwork:     false,
		}
		wrapped, err := runner.Wrap(argv)
		if err != nil {
			return lsp.ClientStartOptions{}, fmt.Errorf("prepare LSP sandbox: %w", err)
		}
		argv = wrapped
	} else if cfg.Policy.RequireLSPIsolation {
		return lsp.ClientStartOptions{}, fmt.Errorf("%w: %s", errLSPIsolationRequired, capability.Reason)
	}
	return lsp.ClientStartOptions{Argv: argv, Directory: workspace}, nil
}

// runWorkspaceWatcher executes the workspace watcher for an LSP client
func (app *App) runWorkspaceWatcher(ctx context.Context, name string, workspaceWatcher *watcher.WorkspaceWatcher) {
	defer app.watcherWG.Done()
	defer logging.RecoverPanic("LSP-"+name, func() {
		// Try to restart the client
		app.restartLSPClient(ctx, name)
	})

	workspaceWatcher.WatchWorkspace(ctx, config.WorkingDirectory())
	logging.Info("Workspace watcher stopped", "client", name)
}

// restartLSPClient attempts to restart a crashed or failed LSP client
func (app *App) restartLSPClient(ctx context.Context, name string) {
	// Get the original configuration
	cfg := config.Get()
	clientConfig, exists := cfg.LSP[name]
	if !exists {
		logging.Error("Cannot restart client, configuration not found", "client", name)
		return
	}
	if !lspClientEnabled(clientConfig) {
		logging.Info("Not restarting disabled LSP client", "name", name)
		return
	}

	// Clean up the old client if it exists
	oldClient, exists := app.LSPClients.Delete(name)

	if exists && oldClient != nil {
		_ = shutdownLSPClient(oldClient)
	}

	// Create a new client using the shared function
	app.createAndStartLSPClient(ctx, name, clientConfig.Command, clientConfig.Args...)
	logging.Info("Successfully restarted LSP client", "client", name)
}

func shutdownLSPClient(client *lsp.Client) error {
	if client == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := client.Shutdown(shutdownCtx)
	if shutdownErr == nil {
		if exitErr := client.Exit(shutdownCtx); exitErr != nil {
			shutdownErr = exitErr
		}
	}
	cancel()
	return errors.Join(shutdownErr, client.Close())
}

func lspClientEnabled(clientConfig config.LSPConfig) bool {
	return !clientConfig.Disabled
}
