package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/opencode-ai/opencode/internal/intelligence"
)

const sentinelToken = "zephyrtokensentinel"

func newTestServer(t *testing.T) (*server.MCPServer, string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"notes.go":  "package notes\n\n// " + sentinelToken + " marks the fixture symbol.\nfunc Fixture() {}\n",
		"README.md": "# Fixture\n\nRepository architecture overview for the test workspace.\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := New(Options{
		Root: root,
		Explain: func(dir string) (any, error) {
			return map[string]string{"workingDirectory": dir}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, root
}

// rpc sends one JSON-RPC request through the server and returns the decoded
// result, or the raw error object when the protocol call failed.
func rpc(t *testing.T, srv *server.MCPServer, id int, method string, params any) (json.RawMessage, json.RawMessage) {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := srv.HandleMessage(context.Background(), data)
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Result, envelope.Error
}

func callTool(t *testing.T, srv *server.MCPServer, id int, name string, arguments map[string]any) mcp.CallToolResult {
	t.Helper()
	result, rpcErr := rpc(t, srv, id, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if rpcErr != nil {
		t.Fatalf("tools/call %s returned JSON-RPC error: %s", name, rpcErr)
	}
	decoded, err := mcp.ParseCallToolResult(&result)
	if err != nil {
		t.Fatalf("decode tools/call %s result: %v", name, err)
	}
	return *decoded
}

func resultText(t *testing.T, result mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("expected one content item, got %d", len(result.Content))
	}
	text, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}

func TestInitializeReportsServerIdentity(t *testing.T) {
	srv, _ := newTestServer(t)
	result, rpcErr := rpc(t, srv, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	if rpcErr != nil {
		t.Fatalf("initialize failed: %s", rpcErr)
	}
	var initialized struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(result, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.ServerInfo.Name != "rsch" || initialized.ServerInfo.Version == "" {
		t.Fatalf("unexpected server info: %+v", initialized.ServerInfo)
	}
}

func TestToolsListExposesOnlyReadOnlyTools(t *testing.T) {
	srv, _ := newTestServer(t)
	result, rpcErr := rpc(t, srv, 2, "tools/list", map[string]any{})
	if rpcErr != nil {
		t.Fatalf("tools/list failed: %s", rpcErr)
	}
	var listed mcp.ListToolsResult
	if err := json.Unmarshal(result, &listed); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"explain_instructions", "get_context", "search_repository"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestSearchRepositoryReturnsMatchWithPath(t *testing.T) {
	srv, _ := newTestServer(t)
	result := callTool(t, srv, 3, "search_repository", map[string]any{"query": sentinelToken})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	var decoded intelligence.Context
	if err := json.Unmarshal([]byte(resultText(t, result)), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	for _, match := range decoded.Matches {
		if match.File == "notes.go" && strings.Contains(match.Text, sentinelToken) {
			return
		}
	}
	t.Fatalf("no match for %q with path notes.go: %+v", sentinelToken, decoded.Matches)
}

func TestGetContextReturnsProvenanceWithinBudget(t *testing.T) {
	srv, _ := newTestServer(t)
	result := callTool(t, srv, 4, "get_context", map[string]any{"query": sentinelToken, "budget": float64(1000)})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	var decoded intelligence.Context
	if err := json.Unmarshal([]byte(resultText(t, result)), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if decoded.Budget != 1000 {
		t.Fatalf("budget = %d, want 1000", decoded.Budget)
	}
	if decoded.Used > decoded.Budget {
		t.Fatalf("used %d exceeds budget %d", decoded.Used, decoded.Budget)
	}
	found := false
	for _, match := range decoded.Matches {
		if match.File == "notes.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("context lacks notes.go provenance: %+v", decoded.Matches)
	}
}

func TestGetContextDefaultsWithoutArguments(t *testing.T) {
	srv, _ := newTestServer(t)
	result := callTool(t, srv, 5, "get_context", map[string]any{})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	var decoded intelligence.Context
	if err := json.Unmarshal([]byte(resultText(t, result)), &decoded); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if decoded.Query != "repository architecture" || decoded.Budget != 4000 {
		t.Fatalf("defaults not applied: query=%q budget=%d", decoded.Query, decoded.Budget)
	}
}

func TestMissingQueryReturnsToolError(t *testing.T) {
	srv, _ := newTestServer(t)
	result := callTool(t, srv, 6, "search_repository", map[string]any{})
	if !result.IsError {
		t.Fatalf("expected isError for missing query, got %s", resultText(t, result))
	}
}

func TestEmptyQueryReturnsToolError(t *testing.T) {
	srv, _ := newTestServer(t)
	result := callTool(t, srv, 7, "search_repository", map[string]any{"query": "   "})
	if !result.IsError {
		t.Fatalf("expected isError for blank query, got %s", resultText(t, result))
	}
}

func TestFractionalLimitReturnsToolError(t *testing.T) {
	srv, _ := newTestServer(t)
	result := callTool(t, srv, 8, "search_repository", map[string]any{"query": sentinelToken, "limit": 2.5})
	if !result.IsError {
		t.Fatalf("expected isError for fractional limit, got %s", resultText(t, result))
	}
}

func TestExplainInstructionsReturnsReport(t *testing.T) {
	srv, root := newTestServer(t)
	result := callTool(t, srv, 9, "explain_instructions", map[string]any{})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(t, result))
	}
	if !strings.Contains(resultText(t, result), root) {
		t.Fatalf("report does not name workspace %q: %s", root, resultText(t, result))
	}
}

func TestNewRejectsMissingRoot(t *testing.T) {
	if _, err := New(Options{Explain: func(string) (any, error) { return nil, nil }}); err == nil {
		t.Fatal("expected error for empty root")
	}
}
