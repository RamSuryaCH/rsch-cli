// Package mcpserver exposes read-only repository inspection tools over the
// Model Context Protocol. The tools reuse the deterministic intelligence index
// that backs `rsch search` and `rsch context`. They never write source files,
// run shell commands, or call a model.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/opencode-ai/opencode/internal/intelligence"
	"github.com/opencode-ai/opencode/internal/version"
)

const (
	serverName = "rsch"

	defaultSearchLimit  = 20
	maxSearchLimit      = 100
	defaultContextQuery = "repository architecture"
	defaultContextLimit = 20
	defaultBudget       = 4000
	minBudget           = 256
	maxBudget           = 32000
)

// Options configures the MCP server for one workspace.
type Options struct {
	// Root is the workspace directory the tools inspect.
	Root string
	// Explain returns the instruction report for a directory. The CLI passes
	// its instruction explainer here so the tool and `rsch instructions explain`
	// share one implementation.
	Explain func(dir string) (any, error)
}

// New builds an MCP server with the read-only repository tools registered.
func New(opts Options) (*server.MCPServer, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("mcp server workspace root is required")
	}
	if opts.Explain == nil {
		return nil, errors.New("mcp server instruction explainer is required")
	}

	s := server.NewMCPServer(serverName, version.Version, server.WithToolCapabilities(false))
	s.AddTools(
		server.ServerTool{Tool: searchRepositoryTool(), Handler: searchRepositoryHandler(opts.Root)},
		server.ServerTool{Tool: getContextTool(), Handler: getContextHandler(opts.Root)},
		server.ServerTool{Tool: explainInstructionsTool(), Handler: explainInstructionsHandler(opts)},
	)
	return s, nil
}

func searchRepositoryTool() mcp.Tool {
	return mcp.NewTool(
		"search_repository",
		mcp.WithDescription("Search repository sources and return matches with file and line provenance as JSON. Read-only."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Text to search for in repository sources.")),
		mcp.WithNumber("limit", mcp.DefaultNumber(defaultSearchLimit), mcp.Min(1), mcp.Max(maxSearchLimit), mcp.Description("Maximum matches to return (1-100).")),
	)
}

func searchRepositoryHandler(root string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.Params.Arguments
		query, err := requiredQuery(args, "query")
		if err != nil {
			return toolError(err), nil
		}
		limit, err := searchLimit(args)
		if err != nil {
			return toolError(err), nil
		}
		result, err := searchIndex(ctx, root, query, limit)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(result)
	}
}

func getContextTool() mcp.Tool {
	return mcp.NewTool(
		"get_context",
		mcp.WithDescription("Return token-budgeted repository context with file/line provenance as JSON. The result is limited to an approximate token budget. Read-only."),
		mcp.WithString("query", mcp.DefaultString(defaultContextQuery), mcp.Description("What to retrieve context for. Defaults to 'repository architecture'.")),
		mcp.WithNumber("budget", mcp.DefaultNumber(defaultBudget), mcp.Min(minBudget), mcp.Max(maxBudget), mcp.Description("Approximate token budget, clamped to 256-32000.")),
		mcp.WithNumber("limit", mcp.DefaultNumber(defaultContextLimit), mcp.Min(1), mcp.Max(maxSearchLimit), mcp.Description("Maximum matches to consider (1-100).")),
	)
}

func getContextHandler(root string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.Params.Arguments
		query := defaultContextQuery
		if _, present := args["query"]; present {
			value, err := requiredQuery(args, "query")
			if err != nil {
				return toolError(err), nil
			}
			query = value
		}
		budget, err := wholeNumber(args, "budget", defaultBudget)
		if err != nil {
			return toolError(err), nil
		}
		budget = clamp(budget, minBudget, maxBudget)
		limit, err := wholeNumber(args, "limit", defaultContextLimit)
		if err != nil {
			return toolError(err), nil
		}
		if limit < 1 {
			return toolError(errors.New("limit must be at least 1")), nil
		}
		limit = clamp(limit, 1, maxSearchLimit)
		index, err := buildIndex(ctx, root)
		if err != nil {
			return toolError(err), nil
		}
		result, err := index.BuildContext(ctx, query, budget, limit)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(result)
	}
}

func explainInstructionsTool() mcp.Tool {
	return mcp.NewTool(
		"explain_instructions",
		mcp.WithDescription("Explain which AGENTS.md and AGENTS.override.md files apply to the workspace, in precedence order, as JSON. Read-only."),
	)
}

func explainInstructionsHandler(opts Options) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := opts.Explain(opts.Root)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(report)
	}
}

func buildIndex(ctx context.Context, root string) (*intelligence.Index, error) {
	index, err := intelligence.New(root)
	if err != nil {
		return nil, err
	}
	if err := index.Build(ctx); err != nil {
		return nil, err
	}
	return index, nil
}

func searchIndex(ctx context.Context, root, query string, limit int) (intelligence.Context, error) {
	index, err := buildIndex(ctx, root)
	if err != nil {
		return intelligence.Context{}, err
	}
	return index.Search(ctx, query, limit)
}

func requiredQuery(args map[string]any, key string) (string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("%s is required", key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	return value, nil
}

func searchLimit(args map[string]any) (int, error) {
	limit, err := wholeNumber(args, "limit", defaultSearchLimit)
	if err != nil {
		return 0, err
	}
	if limit < 1 {
		return 0, errors.New("limit must be at least 1")
	}
	return clamp(limit, 1, maxSearchLimit), nil
}

// wholeNumber reads an optional integer argument. JSON numbers decode as
// float64, so the value must be whole. Values outside the int range are
// clamped before conversion so the conversion cannot overflow.
func wholeNumber(args map[string]any, key string, fallback int) (int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback, nil
	}
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.Trunc(value) != value {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	const bound = 1e9
	value = math.Max(-bound, math.Min(bound, value))
	return int(value), nil
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(err.Error())},
		IsError: true,
	}
}

func jsonResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("encode result: %w", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}
