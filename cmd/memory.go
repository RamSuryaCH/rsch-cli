package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/spf13/cobra"
)

var memoryCmd = &cobra.Command{Use: "memory", Short: "Inspect and manage scoped project memory"}
var memoryAddCmd = &cobra.Command{Use: "add <content>", Short: "Add an entry to project memory", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := memory.New(root)
	if err != nil {
		return err
	}
	category, _ := cmd.Flags().GetString("category")
	scope, _ := cmd.Flags().GetString("scope")
	tags, _ := cmd.Flags().GetStringSlice("tag")
	entry, err := store.Add(memory.Entry{Content: args[0], Category: category, Scope: scope, Tags: tags, Source: "cli"})
	if err != nil {
		return err
	}
	fmt.Println(entry.ID)
	return nil
}}
var memoryListCmd = &cobra.Command{Use: "list", Short: "List project memory entries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := memory.New(root)
	if err != nil {
		return err
	}
	scope, _ := cmd.Flags().GetString("scope")
	items, err := store.List(scope)
	if err != nil {
		return err
	}
	return printMemory(cmd, items)
}}
var memorySearchCmd = &cobra.Command{Use: "search <query>", Short: "Search project memory entries", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := memory.New(root)
	if err != nil {
		return err
	}
	scope, _ := cmd.Flags().GetString("scope")
	items, err := store.Search(args[0], scope)
	if err != nil {
		return err
	}
	return printMemory(cmd, items)
}}
var memoryRemoveCmd = &cobra.Command{Use: "remove <id>", Short: "Remove an entry from project memory", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := memory.New(root)
	if err != nil {
		return err
	}
	return store.Remove(args[0])
}}
var memoryExplainCmd = &cobra.Command{Use: "explain <id>", Short: "Explain one project memory entry", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	store, err := memory.New(root)
	if err != nil {
		return err
	}
	entry, err := store.Explain(args[0])
	if err != nil {
		return err
	}
	return printMemory(cmd, []memory.Entry{entry})
}}

func printMemory(cmd *cobra.Command, entries []memory.Entry) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(entries) == 0 {
		fmt.Println("no memory entries")
		return nil
	}
	for _, entry := range entries {
		fmt.Printf("%s [%s/%s] %s", entry.ID, entry.Scope, entry.Category, entry.Content)
		if len(entry.Tags) > 0 {
			fmt.Printf(" tags=%s", strings.Join(entry.Tags, ","))
		}
		fmt.Println()
	}
	return nil
}

func init() {
	memoryAddCmd.Flags().String("category", "fact", "memory category")
	memoryAddCmd.Flags().String("scope", "repository", "memory scope")
	memoryAddCmd.Flags().StringSlice("tag", nil, "memory tags")
	for _, command := range []*cobra.Command{memoryListCmd, memorySearchCmd, memoryExplainCmd} {
		command.Flags().String("scope", "", "filter by scope")
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	memoryCmd.AddCommand(memoryAddCmd, memoryListCmd, memorySearchCmd, memoryRemoveCmd, memoryExplainCmd)
	rootCmd.AddCommand(memoryCmd)
}
