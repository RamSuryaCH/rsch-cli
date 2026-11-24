package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/network"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/version"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	mcpInitializeTimeout = 10 * time.Second
	mcpListTimeout       = 15 * time.Second
	mcpCallTimeout       = 2 * time.Minute
)

var (
	ErrMCPWorkspaceUntrusted   = errors.New("MCP startup requires a trusted workspace")
	ErrMCPGovernanceDenied     = errors.New("MCP startup requires explicit governance approval")
	ErrMCPIsolationUnavailable = errors.New("MCP stdio startup requires an isolated streaming transport")
)

type mcpTool struct {
	mcpName     string
	tool        mcp.Tool
	mcpConfig   config.MCPServer
	permissions permission.Service
}

// discoveredMCPTool deliberately excludes the permission service. Discovery
// can be cached across agents, while every returned tool must remain bound to
// the permission service supplied by its caller.
type discoveredMCPTool struct {
	mcpName   string
	tool      mcp.Tool
	mcpConfig config.MCPServer
}

type MCPClient interface {
	Initialize(context.Context, mcp.InitializeRequest) (*mcp.InitializeResult, error)
	ListTools(context.Context, mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
	CallTool(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	Close() error
}

type mcpClientStarter interface {
	Start(context.Context) error
}

func (b *mcpTool) Info() tools.ToolInfo {
	return tools.ToolInfo{
		Name:        fmt.Sprintf("%s_%s", b.mcpName, b.tool.Name),
		Description: b.tool.Description,
		Parameters:  b.tool.InputSchema.Properties,
		Required:    b.tool.InputSchema.Required,
	}
}

func withMCPDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}

func initializeMCPClient(ctx context.Context, c MCPClient) error {
	if c == nil {
		return errors.New("initialize MCP client: client is nil")
	}
	operationCtx, cancel := withMCPDeadline(ctx, mcpInitializeTimeout)
	defer cancel()
	if starter, ok := c.(mcpClientStarter); ok {
		if err := starter.Start(operationCtx); err != nil {
			return fmt.Errorf("start MCP transport: %w", err)
		}
	}
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "RSCH", Version: version.Version}
	result, err := c.Initialize(operationCtx, request)
	if err != nil {
		return fmt.Errorf("initialize MCP client: %w", err)
	}
	if result == nil {
		return errors.New("initialize MCP client: server returned an empty result")
	}
	return nil
}

func closeMCPClient(c MCPClient) {
	if c != nil {
		if err := c.Close(); err != nil {
			logging.Warn("failed to close MCP client", "error", err)
		}
	}
}

func renderMCPContent(content []mcp.Content) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch value := item.(type) {
		case mcp.TextContent:
			parts = append(parts, value.Text)
		case *mcp.TextContent:
			if value == nil {
				parts = append(parts, "null")
			} else {
				parts = append(parts, value.Text)
			}
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				parts = append(parts, fmt.Sprintf("%v", value))
				continue
			}
			parts = append(parts, string(encoded))
		}
	}
	return strings.Join(parts, "\n")
}

func decodeMCPArguments(input string) (map[string]any, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("error parsing parameters: %w", err)
	}
	if args == nil {
		return nil, errors.New("error parsing parameters: expected an object")
	}
	return args, nil
}

func runTool(ctx context.Context, c MCPClient, toolName string, input string) (tools.ToolResponse, error) {
	args, err := decodeMCPArguments(input)
	if err != nil {
		closeMCPClient(c)
		return tools.NewTextErrorResponse(err.Error()), nil
	}
	return runToolWithArguments(ctx, c, toolName, args)
}

func runToolWithArguments(ctx context.Context, c MCPClient, toolName string, args map[string]any) (tools.ToolResponse, error) {
	defer closeMCPClient(c)
	if err := initializeMCPClient(ctx, c); err != nil {
		return mcpFailureResponse(err)
	}

	request := mcp.CallToolRequest{}
	request.Params.Name = toolName
	request.Params.Arguments = args
	callCtx, cancel := withMCPDeadline(ctx, mcpCallTimeout)
	defer cancel()
	result, err := c.CallTool(callCtx, request)
	if err != nil {
		return mcpFailureResponse(err)
	}
	if result == nil {
		return tools.NewTextErrorResponse("MCP server returned an empty tool result"), nil
	}
	output := renderMCPContent(result.Content)
	if result.IsError {
		return tools.NewTextErrorResponse(output), nil
	}
	return tools.NewTextResponse(output), nil
}

func mcpFailureResponse(err error) (tools.ToolResponse, error) {
	response := tools.NewTextErrorResponse(err.Error())
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, permission.ErrorPermissionDenied) {
		return response, err
	}
	return response, nil
}

func (b *mcpTool) Run(ctx context.Context, params tools.ToolCall) (tools.ToolResponse, error) {
	sessionID, messageID := tools.GetContextValues(ctx)
	if sessionID == "" || messageID == "" {
		return tools.ToolResponse{}, fmt.Errorf("session ID and message ID are required for executing an MCP tool")
	}
	loaded := config.Get()
	if err := authorizeMCPStartup(loaded, b.mcpName, b.mcpConfig); err != nil {
		return mcpFailureResponse(fmt.Errorf("%w: %w", permission.ErrorPermissionDenied, err))
	}
	args, err := decodeMCPArguments(params.Input)
	if err != nil {
		return tools.NewTextErrorResponse(err.Error()), nil
	}
	if b.permissions == nil {
		return mcpFailureResponse(fmt.Errorf("%w: MCP permission service is unavailable", permission.ErrorPermissionDenied))
	}
	permissionDescription := fmt.Sprintf("execute %s with the following parameters: %s", b.Info().Name, params.Input)
	if !b.permissions.RequestContext(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        config.WorkingDirectory(),
		ToolName:    b.Info().Name,
		Action:      "execute",
		Description: permissionDescription,
		Params:      params.Input,
		ScopeKind:   permission.ScopeDirectory,
	}) {
		if err := ctx.Err(); err != nil {
			return mcpFailureResponse(err)
		}
		return mcpFailureResponse(permission.ErrorPermissionDenied)
	}
	operationCtx, cancel := withMCPDeadline(ctx, mcpCallTimeout)
	defer cancel()
	c, err := newMCPClient(operationCtx, b.mcpName, b.mcpConfig, loaded)
	if err != nil {
		return mcpFailureResponse(err)
	}
	return runToolWithArguments(operationCtx, c, b.tool.Name, args)
}

func NewMcpTool(name string, tool mcp.Tool, permissions permission.Service, mcpConfig config.MCPServer) tools.BaseTool {
	return &mcpTool{mcpName: name, tool: tool, mcpConfig: mcpConfig, permissions: permissions}
}

// stdioMCPClient uses the repository's process boundary instead of mcp-go's
// stdio constructor. The latter unconditionally inherits os.Environ, which
// would expose unrelated provider and cloud credentials to an MCP subprocess.
type stdioMCPClient struct {
	pipes       process.Pipes
	reader      *bufio.Reader
	requestMu   sync.Mutex
	closeOnce   sync.Once
	closeErr    error
	requestID   int64
	initialized bool
}

func newStdioMCPClient(ctx context.Context, server config.MCPServer) (MCPClient, error) {
	environment := sanitizedMCPEnvironment(server.Env)
	pipes, err := process.StartPipes(ctx, process.Spec{
		Argv:             append([]string{server.Command}, server.Args...),
		Directory:        config.WorkingDirectory(),
		Environment:      environment,
		ClearEnvironment: true,
	})
	if err != nil {
		return nil, fmt.Errorf("start MCP stdio server: %w", err)
	}
	go func() {
		_, _ = io.Copy(io.Discard, pipes.Stderr)
	}()
	return &stdioMCPClient{pipes: pipes, reader: bufio.NewReader(pipes.Stdout)}, nil
}

func sanitizedMCPEnvironment(configured []string) map[string]string {
	// Retain only process-bootstrap variables. Credentials and agent-specific
	// state are opt-in through the MCP server's explicit env entries.
	environment := make(map[string]string)
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TERM", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value := os.Getenv(key); value != "" {
			environment[key] = value
		}
	}
	for _, entry := range configured {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "\x00=\r\n") || strings.ContainsAny(value, "\x00") {
			continue
		}
		environment[key] = value
	}
	return environment
}

func (c *stdioMCPClient) Initialize(ctx context.Context, request mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	var result mcp.InitializeResult
	if err := c.request(ctx, "initialize", request.Params, &result); err != nil {
		return nil, err
	}
	if result.ProtocolVersion != "" && result.ProtocolVersion != mcp.LATEST_PROTOCOL_VERSION {
		return nil, fmt.Errorf("MCP server negotiated unsupported protocol version %q", result.ProtocolVersion)
	}
	if err := c.notify("notifications/initialized", struct{}{}); err != nil {
		return nil, err
	}
	c.initialized = true
	return &result, nil
}

func (c *stdioMCPClient) ListTools(ctx context.Context, request mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	if !c.initialized {
		return nil, errors.New("MCP client is not initialized")
	}
	var result mcp.ListToolsResult
	if err := c.request(ctx, "tools/list", request.Params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *stdioMCPClient) CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !c.initialized {
		return nil, errors.New("MCP client is not initialized")
	}
	raw, err := c.requestRaw(ctx, "tools/call", request.Params)
	if err != nil {
		return nil, err
	}
	return mcp.ParseCallToolResult(&raw)
}

func (c *stdioMCPClient) request(ctx context.Context, method string, params any, result any) error {
	raw, err := c.requestRaw(ctx, method, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("decode MCP %s response: %w", method, err)
	}
	return nil
}

func (c *stdioMCPClient) requestRaw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	c.requestID++
	id := c.requestID
	request := mcp.JSONRPCRequest{JSONRPC: mcp.JSONRPC_VERSION, ID: id, Request: mcp.Request{Method: method}, Params: params}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := c.pipes.Stdin.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write MCP %s request: %w", method, err)
	}
	type response struct {
		result json.RawMessage
		err    error
	}
	responseCh := make(chan response, 1)
	go func() {
		for {
			line, readErr := c.reader.ReadBytes('\n')
			if readErr != nil {
				responseCh <- response{err: fmt.Errorf("read MCP %s response: %w", method, readErr)}
				return
			}
			var envelope struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
					Data    any    `json:"data,omitempty"`
				} `json:"error,omitempty"`
			}
			if json.Unmarshal(line, &envelope) != nil || envelope.Method != "" || len(envelope.ID) == 0 {
				continue
			}
			var responseID int64
			if json.Unmarshal(envelope.ID, &responseID) != nil || responseID != id {
				continue
			}
			if envelope.Error != nil {
				responseCh <- response{err: fmt.Errorf("MCP error %d: %s", envelope.Error.Code, envelope.Error.Message)}
				return
			}
			responseCh <- response{result: envelope.Result}
			return
		}
	}()
	select {
	case <-ctx.Done():
		_ = c.pipes.Handle.Kill()
		return nil, ctx.Err()
	case value := <-responseCh:
		return value.result, value.err
	}
}

func (c *stdioMCPClient) notify(method string, params any) error {
	data, err := json.Marshal(mcp.JSONRPCNotification{JSONRPC: mcp.JSONRPC_VERSION, Notification: mcp.Notification{Method: method, Params: mcp.NotificationParams{AdditionalFields: map[string]any{}}}})
	if err != nil {
		return err
	}
	_ = params // Reserved for notifications whose payload is supported later.
	if _, err := c.pipes.Stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write MCP %s notification: %w", method, err)
	}
	return nil
}

func (c *stdioMCPClient) Close() error {
	c.closeOnce.Do(func() {
		if c.pipes.Stdin != nil {
			_ = c.pipes.Stdin.Close()
		}
		if c.pipes.Handle != nil {
			killErr := c.pipes.Handle.Kill()
			_, waitErr := c.pipes.Handle.Wait()
			if killErr != nil {
				c.closeErr = killErr
			} else if waitErr != nil {
				// A forced shutdown normally reports a non-zero exit; lifecycle
				// ownership matters here, not the helper's termination status.
				c.closeErr = nil
			}
		}
		if c.pipes.Stdout != nil {
			_ = c.pipes.Stdout.Close()
		}
		if c.pipes.Stderr != nil {
			_ = c.pipes.Stderr.Close()
		}
	})
	return c.closeErr
}

var (
	stdioMCPClientFactory = newStdioMCPClient
	sseMCPClientFactory   = func(_ context.Context, server config.MCPServer) (MCPClient, error) {
		return client.NewSSEMCPClient(server.URL, client.WithHeaders(server.Headers), client.WithSSEReadTimeout(mcpCallTimeout))
	}
)

func explicitlyApprovedMCPServer(loaded *config.Config, name string) bool {
	if loaded == nil || strings.TrimSpace(name) == "" {
		return false
	}
	for _, approved := range loaded.Governance.ApprovedMCPServers {
		if strings.TrimSpace(approved) == name {
			return true
		}
	}
	return false
}

// authorizeMCPStartup is the mandatory pre-construction boundary. Stdio MCP
// servers are denied until the repository has a streaming transport backed by
// the platform sandbox; process-group containment alone does not isolate the
// workspace or network. Remote SSE does not launch a local executable, but it
// still requires workspace trust, explicit governance approval, and network
// policy validation.
func authorizeMCPStartup(loaded *config.Config, name string, server config.MCPServer) error {
	if loaded == nil || !loaded.WorkspaceTrusted {
		return ErrMCPWorkspaceUntrusted
	}
	if !explicitlyApprovedMCPServer(loaded, name) {
		return fmt.Errorf("%w: %s", ErrMCPGovernanceDenied, name)
	}
	switch server.Type {
	case config.MCPStdio:
		return ErrMCPIsolationUnavailable
	case config.MCPSse:
		if loaded.Policy.RequireMCPIsolation {
			return ErrMCPIsolationUnavailable
		}
		policy := network.Policy{AllowedHosts: loaded.Network.AllowedHosts, AllowPrivate: loaded.Network.AllowPrivate}
		if err := policy.ValidateURL(server.URL); err != nil {
			return fmt.Errorf("MCP SSE startup denied by network policy: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("invalid MCP type %q", server.Type)
	}
}

func newMCPClient(ctx context.Context, name string, server config.MCPServer, loaded *config.Config) (MCPClient, error) {
	if err := authorizeMCPStartup(loaded, name, server); err != nil {
		return nil, err
	}
	switch server.Type {
	case config.MCPStdio:
		// authorizeMCPStartup currently rejects this path. Keep the constructor
		// behind the authorization boundary for a future isolated streaming
		// transport without permitting an unisolated fallback.
		return stdioMCPClientFactory(ctx, server)
	case config.MCPSse:
		return sseMCPClientFactory(ctx, server)
	default:
		return nil, fmt.Errorf("invalid MCP type %q", server.Type)
	}
}

func getTools(ctx context.Context, name string, server config.MCPServer, c MCPClient) ([]discoveredMCPTool, error) {
	defer closeMCPClient(c)
	if err := initializeMCPClient(ctx, c); err != nil {
		return nil, err
	}
	listCtx, cancel := withMCPDeadline(ctx, mcpListTimeout)
	defer cancel()
	request := mcp.ListToolsRequest{}
	seenCursors := make(map[mcp.Cursor]struct{})
	items := make([]discoveredMCPTool, 0)
	for {
		result, err := c.ListTools(listCtx, request)
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		if result == nil {
			return nil, errors.New("list MCP tools: server returned an empty result")
		}
		for _, tool := range result.Tools {
			items = append(items, discoveredMCPTool{mcpName: name, tool: tool, mcpConfig: server})
		}
		if result.NextCursor == "" {
			break
		}
		if _, duplicate := seenCursors[result.NextCursor]; duplicate {
			return nil, fmt.Errorf("list MCP tools: server repeated pagination cursor %q", result.NextCursor)
		}
		seenCursors[result.NextCursor] = struct{}{}
		request.Params.Cursor = result.NextCursor
	}
	return items, nil
}

func materializeMCPTools(discovered []discoveredMCPTool, permissions permission.Service) []tools.BaseTool {
	items := make([]tools.BaseTool, 0, len(discovered))
	for _, item := range discovered {
		items = append(items, NewMcpTool(item.mcpName, item.tool, permissions, item.mcpConfig))
	}
	return items
}

var mcpToolCache struct {
	sync.Mutex
	key   string
	valid bool
	tools []discoveredMCPTool
}

// InvalidateMCPTools forces the next discovery call to rebuild the tool list.
// The configuration fingerprint also invalidates automatically after add/remove.
func InvalidateMCPTools() {
	mcpToolCache.Lock()
	defer mcpToolCache.Unlock()
	mcpToolCache.key = ""
	mcpToolCache.valid = false
	mcpToolCache.tools = nil
}

func mcpFingerprint(loaded *config.Config) string {
	if loaded == nil {
		return ""
	}
	payload := struct {
		Servers          map[string]config.MCPServer `json:"servers"`
		Approved         []string                    `json:"approved"`
		Network          config.NetworkConfig        `json:"network"`
		RequireIsolation bool                        `json:"requireIsolation"`
		Trusted          bool                        `json:"trusted"`
	}{loaded.MCPServers, loaded.Governance.ApprovedMCPServers, loaded.Network, loaded.Policy.RequireMCPIsolation, loaded.WorkspaceTrusted}
	data, _ := json.Marshal(payload)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func GetMcpTools(ctx context.Context, permissions permission.Service) []tools.BaseTool {
	loaded := config.Get()
	if loaded == nil {
		logging.Warn("MCP discovery skipped because configuration is unavailable")
		return nil
	}
	key := mcpFingerprint(loaded)
	mcpToolCache.Lock()
	defer mcpToolCache.Unlock()
	if mcpToolCache.valid && mcpToolCache.key == key {
		return materializeMCPTools(mcpToolCache.tools, permissions)
	}
	if len(loaded.MCPServers) > 0 && !loaded.WorkspaceTrusted {
		logging.Warn("MCP discovery skipped until the workspace is explicitly trusted")
		mcpToolCache.key, mcpToolCache.valid, mcpToolCache.tools = key, true, nil
		return nil
	}

	names := make([]string, 0, len(loaded.MCPServers))
	for name := range loaded.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	discovered := make([]discoveredMCPTool, 0)
	complete := true
	for _, name := range names {
		server := loaded.MCPServers[name]
		if err := authorizeMCPStartup(loaded, name, server); err != nil {
			logging.Warn("MCP server startup denied", "server", name, "error", err)
			continue
		}
		operationCtx, cancel := withMCPDeadline(ctx, mcpInitializeTimeout+mcpListTimeout)
		clientInstance, err := newMCPClient(operationCtx, name, server, loaded)
		if err != nil {
			cancel()
			complete = false
			logging.Error("error creating MCP client", "server", name, "error", err)
			continue
		}
		items, err := getTools(operationCtx, name, server, clientInstance)
		cancel()
		if err != nil {
			complete = false
			logging.Error("error discovering MCP tools", "server", name, "error", err)
			continue
		}
		discovered = append(discovered, items...)
	}
	if complete {
		mcpToolCache.key = key
		mcpToolCache.valid = true
		mcpToolCache.tools = append([]discoveredMCPTool(nil), discovered...)
	} else {
		mcpToolCache.key = ""
		mcpToolCache.valid = false
		mcpToolCache.tools = nil
	}
	return materializeMCPTools(discovered, permissions)
}
