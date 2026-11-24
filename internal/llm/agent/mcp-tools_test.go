package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("RSCH_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": mcp.JSONRPC_VERSION,
				"id":      request.ID,
				"result": map[string]any{
					"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "test-helper", "version": "1"},
				},
			})
		case "tools/call":
			isolated := os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("MCP_EXPLICIT") == "visible"
			content := "environment isolated"
			if !isolated {
				content = "environment leaked or explicit value missing"
			}
			_ = encoder.Encode(map[string]any{
				"jsonrpc": mcp.JSONRPC_VERSION,
				"id":      request.ID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": content}},
					"isError": !isolated,
				},
			})
			return
		}
	}
}

type fakeMCPClient struct {
	startFn      func(context.Context) error
	initializeFn func(context.Context, mcp.InitializeRequest) (*mcp.InitializeResult, error)
	listToolsFn  func(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
	callToolFn   func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	closeCalls   int
	startCalls   int
	initCalls    int
	listCalls    int
	callCalls    int
}

func (c *fakeMCPClient) Start(ctx context.Context) error {
	c.startCalls++
	if c.startFn != nil {
		return c.startFn(ctx)
	}
	return nil
}

func (c *fakeMCPClient) Initialize(ctx context.Context, request mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	c.initCalls++
	if c.initializeFn != nil {
		return c.initializeFn(ctx, request)
	}
	return &mcp.InitializeResult{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION}, nil
}

func (c *fakeMCPClient) ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	c.listCalls++
	if c.listToolsFn != nil {
		return c.listToolsFn(ctx, request)
	}
	return &mcp.ListToolsResult{}, nil
}

func (c *fakeMCPClient) CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c.callCalls++
	if c.callToolFn != nil {
		return c.callToolFn(ctx, request)
	}
	return &mcp.CallToolResult{}, nil
}

func (c *fakeMCPClient) Close() error {
	c.closeCalls++
	return nil
}

func TestInitializeMCPClientStartsTransportFirstAndSetsDeadline(t *testing.T) {
	var order []string
	client := &fakeMCPClient{
		startFn: func(ctx context.Context) error {
			order = append(order, "start")
			return nil
		},
		initializeFn: func(ctx context.Context, request mcp.InitializeRequest) (*mcp.InitializeResult, error) {
			order = append(order, "initialize")
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("initialize context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > mcpInitializeTimeout {
				t.Fatalf("unexpected initialize deadline: %s", remaining)
			}
			if request.Params.ProtocolVersion != mcp.LATEST_PROTOCOL_VERSION {
				t.Fatalf("protocol version = %q", request.Params.ProtocolVersion)
			}
			return &mcp.InitializeResult{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION}, nil
		},
	}

	if err := initializeMCPClient(context.Background(), client); err != nil {
		t.Fatalf("initializeMCPClient() error = %v", err)
	}
	if strings.Join(order, ",") != "start,initialize" {
		t.Fatalf("operation order = %v", order)
	}
}

func TestRunToolConcatenatesAllContentAndCloses(t *testing.T) {
	client := &fakeMCPClient{callToolFn: func(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if request.Params.Name != "lookup" {
			t.Fatalf("tool name = %q", request.Params.Name)
		}
		if request.Params.Arguments["query"] != "needle" {
			t.Fatalf("arguments = %#v", request.Params.Arguments)
		}
		second := mcp.NewTextContent("second")
		return &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("first"), &second}}, nil
	}}

	response, err := runTool(context.Background(), client, "lookup", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("runTool() error = %v", err)
	}
	if response.IsError || response.Content != "first\nsecond" {
		t.Fatalf("response = %#v", response)
	}
	if client.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", client.closeCalls)
	}
}

func TestRunToolPreservesMCPErrorResult(t *testing.T) {
	client := &fakeMCPClient{callToolFn: func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{mcp.NewTextContent("server rejected request")},
			IsError: true,
		}, nil
	}}

	response, err := runTool(context.Background(), client, "lookup", `{}`)
	if err != nil {
		t.Fatalf("runTool() error = %v", err)
	}
	if !response.IsError || response.Content != "server rejected request" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRunToolRejectsInvalidInputBeforeContactingServer(t *testing.T) {
	for _, input := range []string{"", "null", `[]`, `"string"`} {
		t.Run(input, func(t *testing.T) {
			client := &fakeMCPClient{}
			response, err := runTool(context.Background(), client, "lookup", input)
			if err != nil {
				t.Fatalf("runTool() error = %v", err)
			}
			if !response.IsError {
				t.Fatalf("response = %#v, want an error", response)
			}
			if client.startCalls != 0 || client.initCalls != 0 || client.callCalls != 0 {
				t.Fatalf("server contacted for invalid input: start=%d initialize=%d call=%d", client.startCalls, client.initCalls, client.callCalls)
			}
			if client.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", client.closeCalls)
			}
		})
	}
}

func TestMCPClientClosesAfterFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(*fakeMCPClient)
		make func() *fakeMCPClient
	}{
		{
			name: "initialize",
			make: func() *fakeMCPClient {
				return &fakeMCPClient{initializeFn: func(context.Context, mcp.InitializeRequest) (*mcp.InitializeResult, error) {
					return nil, errors.New("initialize failed")
				}}
			},
			run: func(client *fakeMCPClient) { _, _ = runTool(context.Background(), client, "lookup", `{}`) },
		},
		{
			name: "call",
			make: func() *fakeMCPClient {
				return &fakeMCPClient{callToolFn: func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return nil, errors.New("call failed")
				}}
			},
			run: func(client *fakeMCPClient) { _, _ = runTool(context.Background(), client, "lookup", `{}`) },
		},
		{
			name: "list",
			make: func() *fakeMCPClient {
				return &fakeMCPClient{listToolsFn: func(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
					return nil, errors.New("list failed")
				}}
			},
			run: func(client *fakeMCPClient) {
				_, _ = getTools(context.Background(), "test", config.MCPServer{}, client)
			},
		},
		{
			name: "empty list result",
			make: func() *fakeMCPClient {
				return &fakeMCPClient{listToolsFn: func(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
					return nil, nil
				}}
			},
			run: func(client *fakeMCPClient) {
				_, _ = getTools(context.Background(), "test", config.MCPServer{}, client)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := test.make()
			test.run(client)
			if client.closeCalls != 1 {
				t.Fatalf("Close() calls = %d, want 1", client.closeCalls)
			}
		})
	}
}

func TestRunToolCancellationClosesBlockingClient(t *testing.T) {
	client := &fakeMCPClient{initializeFn: func(ctx context.Context, _ mcp.InitializeRequest) (*mcp.InitializeResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	started := time.Now()
	response, err := runTool(ctx, client, "lookup", `{}`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runTool() error = %v, want deadline exceeded", err)
	}
	if !response.IsError || !strings.Contains(response.Content, context.DeadlineExceeded.Error()) {
		t.Fatalf("response = %#v", response)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	if client.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", client.closeCalls)
	}
}

func TestMCPCallCancellationRemainsTerminal(t *testing.T) {
	client := &fakeMCPClient{callToolFn: func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, context.Canceled
	}}
	response, err := runTool(context.Background(), client, "lookup", `{}`)
	if !errors.Is(err, context.Canceled) || !response.IsError {
		t.Fatalf("cancellation became a recoverable tool response: response=%+v, error=%v", response, err)
	}
	if client.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", client.closeCalls)
	}
}

func TestMCPStartupDenialStopsRemainingToolCalls(t *testing.T) {
	// Stdio is always rejected until an isolated transport is available.
	// This fixture never constructs a client or contacts a server.
	denied := NewMcpTool("denied", mcp.Tool{Name: "fixture"}, nil, config.MCPServer{Type: config.MCPStdio})
	nextRan := false
	next := testTool{
		info: tools.ToolInfo{Name: "next"},
		run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
			nextRan = true
			return tools.NewTextResponse("must not execute"), nil
		},
	}
	a := newTestAgent(&testProvider{}, denied, next)
	defer a.Shutdown()
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, "session-1")
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, "message-1")
	results, err := a.executeToolCalls(ctx, []message.ToolCall{
		{ID: "denied-call", Name: denied.Info().Name, Input: `{}`},
		{ID: "next-call", Name: "next", Input: `{}`},
	})
	if !errors.Is(err, permission.ErrorPermissionDenied) {
		t.Fatalf("MCP startup denial error = %v, want permission denied", err)
	}
	if nextRan {
		t.Fatal("agent executed a later tool after MCP startup was denied")
	}
	if len(results) != 2 || results[1].ToolCallID != "next-call" || !results[1].IsError {
		t.Fatalf("missing canceled result for the remaining tool: %+v", results)
	}
}

func TestSanitizedMCPEnvironment(t *testing.T) {
	t.Setenv("PATH", "/safe/bin")
	t.Setenv("OPENAI_API_KEY", "inherited-secret")

	environment := sanitizedMCPEnvironment([]string{"MCP_TOKEN=explicit", "INVALID", "=missing-key", "BAD\nKEY=value"})
	if environment["PATH"] != "/safe/bin" {
		t.Fatalf("PATH = %q", environment["PATH"])
	}
	if _, ok := environment["OPENAI_API_KEY"]; ok {
		t.Fatal("inherited OPENAI_API_KEY leaked into MCP environment")
	}
	if environment["MCP_TOKEN"] != "explicit" {
		t.Fatalf("MCP_TOKEN = %q", environment["MCP_TOKEN"])
	}
	if len(environment) == 0 {
		t.Fatal("sanitized environment is unexpectedly empty")
	}

	explicit := sanitizedMCPEnvironment([]string{"OPENAI_API_KEY=server-specific"})
	if explicit["OPENAI_API_KEY"] != "server-specific" {
		t.Fatalf("explicit OPENAI_API_KEY = %q", explicit["OPENAI_API_KEY"])
	}
}

func TestStdioMCPClientUsesSanitizedProcessBoundary(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := newStdioMCPClient(ctx, config.MCPServer{
		Type:    config.MCPStdio,
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Env:     []string{"RSCH_MCP_HELPER=1", "MCP_EXPLICIT=visible"},
	})
	if err != nil {
		t.Fatalf("newStdioMCPClient() error = %v", err)
	}
	response, err := runTool(ctx, client, "check_environment", `{}`)
	if err != nil {
		t.Fatalf("runTool() error = %v", err)
	}
	if response.IsError || response.Content != "environment isolated" {
		t.Fatalf("response = %#v", response)
	}
}

func TestMCPStartupDeniesBeforeClientFactory(t *testing.T) {
	originalStdioFactory := stdioMCPClientFactory
	originalSSEFactory := sseMCPClientFactory
	defer func() {
		stdioMCPClientFactory = originalStdioFactory
		sseMCPClientFactory = originalSSEFactory
	}()
	stdioStarts := 0
	sseStarts := 0
	stdioMCPClientFactory = func(context.Context, config.MCPServer) (MCPClient, error) {
		stdioStarts++
		return &fakeMCPClient{}, nil
	}
	sseMCPClientFactory = func(context.Context, config.MCPServer) (MCPClient, error) {
		sseStarts++
		return &fakeMCPClient{}, nil
	}

	tests := []struct {
		name   string
		cfg    *config.Config
		server config.MCPServer
		want   error
	}{
		{
			name:   "configuration unavailable",
			server: config.MCPServer{Type: config.MCPStdio, Command: "must-not-run"},
			want:   ErrMCPWorkspaceUntrusted,
		},
		{
			name: "workspace untrusted",
			cfg: &config.Config{
				Governance: config.GovernanceConfig{ApprovedMCPServers: []string{"project"}},
			},
			server: config.MCPServer{Type: config.MCPStdio, Command: "must-not-run"},
			want:   ErrMCPWorkspaceUntrusted,
		},
		{
			name: "approval list empty",
			cfg: &config.Config{
				WorkspaceTrusted: true,
			},
			server: config.MCPServer{Type: config.MCPStdio, Command: "must-not-run"},
			want:   ErrMCPGovernanceDenied,
		},
		{
			name: "different server approved",
			cfg: &config.Config{
				WorkspaceTrusted: true,
				Governance:       config.GovernanceConfig{ApprovedMCPServers: []string{"other"}},
			},
			server: config.MCPServer{Type: config.MCPStdio, Command: "must-not-run"},
			want:   ErrMCPGovernanceDenied,
		},
		{
			name: "stdio lacks isolated streaming transport",
			cfg: &config.Config{
				WorkspaceTrusted: true,
				Governance:       config.GovernanceConfig{ApprovedMCPServers: []string{"project"}},
			},
			server: config.MCPServer{Type: config.MCPStdio, Command: "must-not-run"},
			want:   ErrMCPIsolationUnavailable,
		},
		{
			name: "SSE isolation policy",
			cfg: &config.Config{
				WorkspaceTrusted: true,
				Governance:       config.GovernanceConfig{ApprovedMCPServers: []string{"project"}},
				Policy:           config.PolicyConfig{RequireMCPIsolation: true},
			},
			server: config.MCPServer{Type: config.MCPSse, URL: "https://example.test/mcp"},
			want:   ErrMCPIsolationUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newMCPClient(context.Background(), "project", test.server, test.cfg)
			if !errors.Is(err, test.want) {
				t.Fatalf("newMCPClient() error = %v, want %v", err, test.want)
			}
			if stdioStarts != 0 || sseStarts != 0 {
				t.Fatalf("client factory invoked after denied startup: stdio=%d sse=%d", stdioStarts, sseStarts)
			}
		})
	}
}

func TestMCPStartupAllowsApprovedPolicyCheckedSSE(t *testing.T) {
	originalFactory := sseMCPClientFactory
	defer func() { sseMCPClientFactory = originalFactory }()
	starts := 0
	sseMCPClientFactory = func(context.Context, config.MCPServer) (MCPClient, error) {
		starts++
		return &fakeMCPClient{}, nil
	}
	loaded := &config.Config{
		WorkspaceTrusted: true,
		Governance:       config.GovernanceConfig{ApprovedMCPServers: []string{"remote"}},
		Network:          config.NetworkConfig{AllowedHosts: []string{"127.0.0.1"}, AllowPrivate: true},
	}
	client, err := newMCPClient(context.Background(), "remote", config.MCPServer{Type: config.MCPSse, URL: "http://127.0.0.1/mcp"}, loaded)
	if err != nil {
		t.Fatalf("newMCPClient() error = %v", err)
	}
	if client == nil || starts != 1 {
		t.Fatalf("approved SSE startup: client=%v starts=%d", client, starts)
	}
}

func TestGetToolsFollowsPaginationAndRejectsCursorCycles(t *testing.T) {
	page := 0
	client := &fakeMCPClient{listToolsFn: func(_ context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
		page++
		switch page {
		case 1:
			if request.Params.Cursor != "" {
				t.Fatalf("first cursor = %q", request.Params.Cursor)
			}
			return &mcp.ListToolsResult{
				Tools:           []mcp.Tool{{Name: "first"}},
				PaginatedResult: mcp.PaginatedResult{NextCursor: "next"},
			}, nil
		case 2:
			if request.Params.Cursor != "next" {
				t.Fatalf("second cursor = %q", request.Params.Cursor)
			}
			return &mcp.ListToolsResult{Tools: []mcp.Tool{{Name: "second"}}}, nil
		default:
			t.Fatalf("unexpected page %d", page)
			return nil, nil
		}
	}}

	discovered, err := getTools(context.Background(), "server", config.MCPServer{}, client)
	if err != nil {
		t.Fatalf("getTools() error = %v", err)
	}
	if len(discovered) != 2 || discovered[0].tool.Name != "first" || discovered[1].tool.Name != "second" {
		t.Fatalf("discovered tools = %#v", discovered)
	}

	cyclic := &fakeMCPClient{listToolsFn: func(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
		return &mcp.ListToolsResult{PaginatedResult: mcp.PaginatedResult{NextCursor: "same"}}, nil
	}}
	if _, err := getTools(context.Background(), "server", config.MCPServer{}, cyclic); err == nil || !strings.Contains(err.Error(), "repeated pagination cursor") {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestMaterializeMCPToolsBindsCurrentPermissionService(t *testing.T) {
	discovered := []discoveredMCPTool{{mcpName: "server", tool: mcp.Tool{Name: "tool"}}}
	firstService := permission.NewPermissionService()
	secondService := permission.NewPermissionService()

	first := materializeMCPTools(discovered, firstService)
	second := materializeMCPTools(discovered, secondService)
	if first[0].(*mcpTool).permissions != firstService {
		t.Fatal("first tool was not bound to the first permission service")
	}
	if second[0].(*mcpTool).permissions != secondService {
		t.Fatal("cached discovery retained a stale permission service")
	}
}

func TestMCPFingerprintAndInvalidation(t *testing.T) {
	first := &config.Config{
		WorkspaceTrusted: true,
		MCPServers: map[string]config.MCPServer{
			"alpha": {Type: config.MCPStdio, Command: "server", Args: []string{"one"}},
			"beta":  {Type: config.MCPSse, URL: "https://example.test/mcp"},
		},
	}
	second := &config.Config{
		WorkspaceTrusted: true,
		MCPServers: map[string]config.MCPServer{
			"beta":  {Type: config.MCPSse, URL: "https://example.test/mcp"},
			"alpha": {Type: config.MCPStdio, Command: "server", Args: []string{"one"}},
		},
	}
	if mcpFingerprint(first) != mcpFingerprint(second) {
		t.Fatal("fingerprint depends on map insertion order")
	}
	second.MCPServers["alpha"] = config.MCPServer{Type: config.MCPStdio, Command: "server", Args: []string{"two"}}
	if mcpFingerprint(first) == mcpFingerprint(second) {
		t.Fatal("fingerprint did not change with server configuration")
	}

	mcpToolCache.Lock()
	mcpToolCache.key = "stale"
	mcpToolCache.valid = true
	mcpToolCache.tools = []discoveredMCPTool{{}}
	mcpToolCache.Unlock()
	InvalidateMCPTools()
	mcpToolCache.Lock()
	defer mcpToolCache.Unlock()
	if mcpToolCache.key != "" || mcpToolCache.valid || mcpToolCache.tools != nil {
		t.Fatalf("cache was not invalidated: key=%q valid=%v tools=%v", mcpToolCache.key, mcpToolCache.valid, mcpToolCache.tools)
	}
}
