package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/plugin"
	"github.com/spf13/cobra"
)

var pluginCmd = &cobra.Command{Use: "plugin", Short: "Manage versioned project plugins"}
var pluginListCmd = &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, _ []string) error {
	m, err := plugin.New(config.WorkingDirectory())
	if err != nil {
		return err
	}
	items, err := m.List()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}}
var pluginInspectCmd = &cobra.Command{Use: "inspect <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	m, err := plugin.New(config.WorkingDirectory())
	if err != nil {
		return err
	}
	item, err := m.Inspect(args[0])
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}}

var pluginInstallCmd = &cobra.Command{Use: "install <name>", Args: cobra.ExactArgs(1), Short: "Install a local plugin manifest", RunE: func(cmd *cobra.Command, args []string) error {
	m, err := plugin.New(config.WorkingDirectory())
	if err != nil {
		return err
	}
	source, _ := cmd.Flags().GetString("source")
	path, err := m.Install(args[0], source)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}}

var pluginRemoveCmd = &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), Short: "Remove an installed plugin manifest", RunE: func(_ *cobra.Command, args []string) error {
	m, err := plugin.New(config.WorkingDirectory())
	if err != nil {
		return err
	}
	if err := m.Remove(args[0]); err != nil {
		return err
	}
	fmt.Printf("plugin %q removed\n", args[0])
	return nil
}}

func init() {
	pluginInstallCmd.Flags().String("source", "", "Local plugin manifest JSON")
	pluginCmd.AddCommand(pluginListCmd, pluginInspectCmd, pluginInstallCmd, pluginRemoveCmd)
	rootCmd.AddCommand(pluginCmd)
}
