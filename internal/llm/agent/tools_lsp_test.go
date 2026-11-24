package agent

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/lsp"
)

func TestCoderReadOnlyToolsAlwaysIncludeDiagnostics(t *testing.T) {
	assembled := coderReadOnlyTools(lsp.NewClientRegistry())
	for _, tool := range assembled {
		if tool.Info().Name == tools.DiagnosticsToolName {
			return
		}
	}
	t.Fatal("diagnostics tool was omitted while LSP initialization was pending")
}
