package tools

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/lsp"
)

func TestDiagnosticsToolSeesClientsAddedAfterAssembly(t *testing.T) {
	registry := lsp.NewClientRegistry()
	tool := NewDiagnosticsToolWithClientProvider(registry)
	before, err := tool.Run(context.Background(), ToolCall{Input: `{}`})
	if err != nil || !before.IsError {
		t.Fatalf("empty registry result = %+v, err=%v", before, err)
	}

	registry.Set("go", &lsp.Client{})
	after, err := tool.Run(context.Background(), ToolCall{Input: `{}`})
	if err != nil || after.IsError {
		t.Fatalf("late client was not visible: result=%+v err=%v", after, err)
	}
}
