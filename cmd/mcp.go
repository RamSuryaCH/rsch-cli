package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/network"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{Use: "mcp", Short: "Manage configured MCP servers"}

type mcpView struct {
	Name    string            `json:"name"`
	Type    config.MCPType    `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     []string          `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func loadedMCPConfig() (*config.Config, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if loaded := config.Get(); loaded != nil {
		return loaded, nil
	}
	return config.Load(root, false)
}

func viewMCP(name string, server config.MCPServer) mcpView {
	view := mcpView{Name: name, Type: server.Type, Command: security.RedactText(server.Command), URL: security.RedactText(server.URL)}
	for _, arg := range server.Args {
		view.Args = append(view.Args, security.RedactText(arg))
	}
	for _, value := range server.Env {
		view.Env = append(view.Env, redactMCPValue(value))
	}
	if len(server.Headers) > 0 {
		view.Headers = map[string]string{}
		for key, value := range server.Headers {
			if sensitiveMCPKey(key) {
				view.Headers[key] = "[REDACTED]"
			} else {
				view.Headers[key] = security.RedactText(value)
			}
		}
	}
	return view
}

func sensitiveMCPKey(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "authorization") || strings.Contains(lower, "key")
}

func redactMCPValue(value string) string {
	if index := strings.IndexByte(value, '='); index > 0 && sensitiveMCPKey(value[:index]) {
		return value[:index+1] + "[REDACTED]"
	}
	return security.RedactText(value)
}

func printMCPViews(cmd *cobra.Command, views []mcpView) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, err := json.MarshalIndent(views, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	for _, view := range views {
		fmt.Printf("%s\t%s", view.Name, view.Type)
		if view.Command != "" {
			fmt.Printf("\t%s", view.Command)
		}
		if view.URL != "" {
			fmt.Printf("\t%s", view.URL)
		}
		fmt.Println()
	}
	return nil
}

var mcpListCmd = &cobra.Command{Use: "list", Short: "List configured MCP servers", RunE: func(cmd *cobra.Command, _ []string) error {
	loaded, err := loadedMCPConfig()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(loaded.MCPServers))
	for name := range loaded.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]mcpView, 0, len(names))
	for _, name := range names {
		views = append(views, viewMCP(name, loaded.MCPServers[name]))
	}
	return printMCPViews(cmd, views)
}}

var mcpInspectCmd = &cobra.Command{Use: "inspect <name>", Args: cobra.ExactArgs(1), Short: "Inspect one MCP server with secrets redacted", RunE: func(cmd *cobra.Command, args []string) error {
	loaded, err := loadedMCPConfig()
	if err != nil {
		return err
	}
	server, ok := loaded.MCPServers[args[0]]
	if !ok {
		return fmt.Errorf("MCP server %q is not configured", args[0])
	}
	return printMCPViews(cmd, []mcpView{viewMCP(args[0], server)})
}}

var mcpAddCmd = &cobra.Command{Use: "add <name>", Args: cobra.ExactArgs(1), Short: "Add or replace an MCP server", RunE: func(cmd *cobra.Command, args []string) error {
	loaded, err := loadedMCPConfig()
	if err != nil {
		return err
	}
	typeName, _ := cmd.Flags().GetString("type")
	server := config.MCPServer{Type: config.MCPType(typeName)}
	server.Command, _ = cmd.Flags().GetString("command")
	server.Args, _ = cmd.Flags().GetStringSlice("arg")
	server.Env, _ = cmd.Flags().GetStringSlice("env")
	server.URL, _ = cmd.Flags().GetString("url")
	headerValues, _ := cmd.Flags().GetStringSlice("header")
	server.Headers = map[string]string{}
	for _, item := range headerValues {
		key, value, found := strings.Cut(item, "=")
		if !found || strings.TrimSpace(key) == "" {
			return fmt.Errorf("header must use KEY=VALUE syntax")
		}
		server.Headers[strings.TrimSpace(key)] = value
	}
	if server.Type == config.MCPSse {
		parsed, parseErr := url.Parse(server.URL)
		if parseErr != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("invalid MCP SSE URL")
		}
		if err := (network.Policy{AllowedHosts: loaded.Network.AllowedHosts, AllowPrivate: loaded.Network.AllowPrivate}).ValidateURL(server.URL); err != nil {
			return fmt.Errorf("MCP SSE endpoint denied by network policy: %w", err)
		}
	}
	if err := config.AddMCPServer(args[0], server); err != nil {
		return err
	}
	fmt.Printf("MCP server %q configured\n", args[0])
	return nil
}}

var mcpRemoveCmd = &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), Short: "Remove an MCP server", RunE: func(_ *cobra.Command, args []string) error {
	if _, err := loadedMCPConfig(); err != nil {
		return err
	}
	if err := config.RemoveMCPServer(args[0]); err != nil {
		return err
	}
	fmt.Printf("MCP server %q removed\n", args[0])
	return nil
}}

func init() {
	for _, command := range []*cobra.Command{mcpListCmd, mcpInspectCmd} {
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	mcpAddCmd.Flags().String("type", "stdio", "MCP server type (stdio or sse)")
	mcpAddCmd.Flags().String("command", "", "stdio server executable")
	mcpAddCmd.Flags().StringSlice("arg", nil, "stdio server argument (repeatable)")
	mcpAddCmd.Flags().StringSlice("env", nil, "server environment entry KEY=VALUE (repeatable)")
	mcpAddCmd.Flags().String("url", "", "SSE server URL")
	mcpAddCmd.Flags().StringSlice("header", nil, "SSE header KEY=VALUE (repeatable)")
	mcpCmd.AddCommand(mcpListCmd, mcpAddCmd, mcpRemoveCmd, mcpInspectCmd)
	rootCmd.AddCommand(mcpCmd)
}
