package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/server"
	"github.com/opencode-ai/opencode/internal/mcpserver"
	"github.com/spf13/cobra"
)

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve read-only repository tools over MCP stdio",
	Long: `Serve read-only repository tools over the Model Context Protocol on stdin and stdout.

Tools: search_repository, get_context, explain_instructions. None of them write
source files, run shell commands, or call a model; search and context refresh
the local index cache under .rsch/intelligence/, as "rsch search" does.
Diagnostics go to stderr.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := mcpServeRoot(cmd)
		if err != nil {
			return err
		}
		srv, err := mcpserver.New(mcpserver.Options{
			Root: root,
			Explain: func(dir string) (any, error) {
				return explainInstructions(dir)
			},
		})
		if err != nil {
			return err
		}
		return server.ServeStdio(srv)
	},
}

// mcpServeRoot resolves the workspace root: the --cwd flag when set, otherwise
// the process working directory.
func mcpServeRoot(cmd *cobra.Command) (string, error) {
	cwd, _ := cmd.Flags().GetString("cwd")
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		return wd, nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve --cwd: %w", err)
	}
	return abs, nil
}

func init() {
	mcpCmd.AddCommand(mcpServeCmd)
}
