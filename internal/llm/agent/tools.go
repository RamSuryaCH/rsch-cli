package agent

import (
	"context"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
)

func CoderAgentTools(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	registries ...*orchestration.Registry,
) []tools.BaseTool {
	return coderAgentTools(permissions, sessions, messages, history, lsp.NewStaticClientProvider(lspClients), nil, nil, registries...)
}

// CoderAgentToolsOnBus is the application-wired variant. It ensures tool
// lifecycle evidence is published to the same bus consumed by hooks, HTTP/SSE,
// audit, and replay surfaces.
func CoderAgentToolsOnBus(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	bus *events.Bus,
	registries ...*orchestration.Registry,
) []tools.BaseTool {
	return coderAgentTools(permissions, sessions, messages, history, lsp.NewStaticClientProvider(lspClients), bus, nil, registries...)
}

// CoderAgentToolsOnBusWithClientProvider keeps the tool set bound to the
// application's live, concurrency-safe LSP registry.
func CoderAgentToolsOnBusWithClientProvider(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients lsp.ClientProvider,
	bus *events.Bus,
	registries ...*orchestration.Registry,
) []tools.BaseTool {
	return coderAgentTools(permissions, sessions, messages, history, lspClients, bus, nil, registries...)
}

// CoderAgentToolsWithExecutor is the dependency-injected variant for callers
// that already own the configured execution/evidence kernel.
func CoderAgentToolsWithExecutor(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	executor *execution.Executor,
	registries ...*orchestration.Registry,
) []tools.BaseTool {
	return coderAgentTools(permissions, sessions, messages, history, lsp.NewStaticClientProvider(lspClients), nil, executor, registries...)
}

// CoderAgentToolsOnBusWithExecutor combines dependency injection with the
// application's shared evidence bus.
func CoderAgentToolsOnBusWithExecutor(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients map[string]*lsp.Client,
	bus *events.Bus,
	executor *execution.Executor,
	registries ...*orchestration.Registry,
) []tools.BaseTool {
	return coderAgentTools(permissions, sessions, messages, history, lsp.NewStaticClientProvider(lspClients), bus, executor, registries...)
}

func coderAgentTools(
	permissions permission.Service,
	sessions session.Service,
	messages message.Service,
	history history.Service,
	lspClients lsp.ClientProvider,
	bus *events.Bus,
	executor *execution.Executor,
	registries ...*orchestration.Registry,
) []tools.BaseTool {
	ctx := context.Background()
	otherTools := GetMcpTools(ctx, permissions)
	var registry *orchestration.Registry
	if len(registries) > 0 {
		registry = registries[0]
	}
	readOnly := coderReadOnlyTools(lspClients)
	all := append(append([]tools.BaseTool{
		tools.NewBashTool(permissions),
		tools.NewEditToolWithClientProvider(lspClients, permissions, history),
		tools.NewFetchTool(permissions),
		tools.NewBrowserTool(permissions),
		tools.NewPatchToolWithClientProvider(lspClients, permissions, history),
		tools.NewWriteToolWithClientProvider(lspClients, permissions, history),
		NewAgentToolOnBusWithClientProvider(sessions, messages, lspClients, bus, registry),
	}, readOnly...), otherTools...)
	// Every coder-agent tool crosses the common execution/evidence boundary.
	// Individual tools retain their existing permission prompts; the executor
	// adds policy, timeout, cancellation, and lifecycle events consistently.
	if executor == nil {
		var executorErr error
		executor, executorErr = tools.ReadOnlyExecutor(bus)
		if executorErr != nil {
			return nil
		}
	}
	governed := make([]tools.BaseTool, 0, len(all))
	for _, tool := range all {
		riskLevel := execution.RiskMedium
		switch tool.Info().Name {
		case tools.GlobToolName, tools.GrepToolName, tools.LSToolName, tools.SourcegraphToolName, tools.ViewToolName, tools.DiagnosticsToolName:
			riskLevel = execution.RiskLow
		case tools.BashToolName, tools.EditToolName, tools.PatchToolName, tools.WriteToolName:
			riskLevel = execution.RiskHigh
		case tools.BrowserToolName:
			riskLevel = execution.RiskHigh
		}
		governed = append(governed, tools.Govern(tool, executor, riskLevel))
	}
	return governed
}

func coderReadOnlyTools(lspClients lsp.ClientProvider) []tools.BaseTool {
	return []tools.BaseTool{
		tools.NewGlobTool(), tools.NewGrepTool(), tools.NewLsTool(),
		tools.NewSourcegraphTool(), tools.NewViewToolWithClientProvider(lspClients),
		tools.NewDiagnosticsToolWithClientProvider(lspClients),
	}
}

func TaskAgentTools(lspClients map[string]*lsp.Client) []tools.BaseTool {
	return taskAgentTools(lsp.NewStaticClientProvider(lspClients), nil, nil)
}

func TaskAgentToolsOnBus(lspClients map[string]*lsp.Client, bus *events.Bus) []tools.BaseTool {
	return taskAgentTools(lsp.NewStaticClientProvider(lspClients), bus, nil)
}

func TaskAgentToolsWithExecutor(lspClients map[string]*lsp.Client, executor *execution.Executor) []tools.BaseTool {
	return taskAgentTools(lsp.NewStaticClientProvider(lspClients), nil, executor)
}

func TaskAgentToolsOnBusWithExecutor(lspClients map[string]*lsp.Client, bus *events.Bus, executor *execution.Executor) []tools.BaseTool {
	return taskAgentTools(lsp.NewStaticClientProvider(lspClients), bus, executor)
}

func TaskAgentToolsOnBusWithClientProvider(lspClients lsp.ClientProvider, bus *events.Bus) []tools.BaseTool {
	return taskAgentTools(lspClients, bus, nil)
}

func taskAgentTools(lspClients lsp.ClientProvider, bus *events.Bus, executor *execution.Executor) []tools.BaseTool {
	raw := []tools.BaseTool{
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewSourcegraphTool(),
		tools.NewViewToolWithClientProvider(lspClients),
	}
	if executor == nil {
		var err error
		executor, err = tools.ReadOnlyExecutor(bus)
		if err != nil {
			return nil
		}
	}
	governed := make([]tools.BaseTool, 0, len(raw))
	for _, tool := range raw {
		governed = append(governed, tools.Govern(tool, executor, execution.RiskLow))
	}
	return governed
}
