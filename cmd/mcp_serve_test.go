package cmd

import "testing"

func TestMCPServeCommandIsRegistered(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"mcp", "serve"})
	if err != nil {
		t.Fatalf("find mcp serve: %v", err)
	}
	if command.Name() != "serve" || command.Parent() != mcpCmd {
		t.Fatalf("mcp serve resolved to %q under %v", command.Name(), command.Parent())
	}
}
