package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/intelligence"
	"github.com/spf13/cobra"
)

var searchCmd = &cobra.Command{Use: "search <query>", Short: "Search repository sources with provenance", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	index, err := intelligence.New(root)
	if err != nil {
		return err
	}
	if err := index.Build(cmd.Context()); err != nil {
		return err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	result, err := index.Search(cmd.Context(), args[0], limit)
	if err != nil {
		return err
	}
	return printSearchResult(cmd, result)
}}

var contextCmd = &cobra.Command{Use: "context [query]", Short: "Retrieve bounded repository context with provenance", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	index, err := intelligence.New(root)
	if err != nil {
		return err
	}
	if err := index.Build(cmd.Context()); err != nil {
		return err
	}
	query := "repository architecture"
	if len(args) == 1 {
		query = args[0]
	}
	limit, _ := cmd.Flags().GetInt("limit")
	budget, _ := cmd.Flags().GetInt("budget")
	result, err := index.BuildContext(cmd.Context(), query, budget, limit)
	if err != nil {
		return err
	}
	return printSearchResult(cmd, result)
}}

func printSearchResult(cmd *cobra.Command, result intelligence.Context) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if result.Budget > 0 {
		fmt.Printf("budget: %d tokens, used: %d, selected: %d, excluded: %d\n", result.Budget, result.Used, len(result.Selected), len(result.Excluded))
	}
	for _, source := range result.Sources {
		location := source.Path
		if location == "" {
			location = "workspace"
		}
		fmt.Printf("source: %s (%s) — %s\n", source.Kind, location, source.Reason)
	}
	if len(result.Matches) == 0 {
		fmt.Println("no matches")
		return nil
	}
	for _, match := range result.Matches {
		fmt.Printf("%s:%d [%d] %s\n", match.File, match.Line, match.Score, match.Text)
	}
	return nil
}

func init() {
	for _, command := range []*cobra.Command{searchCmd, contextCmd} {
		command.Flags().Int("limit", 20, "Maximum matches")
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	contextCmd.Flags().Int("budget", 4000, "Approximate context token budget")
	rootCmd.AddCommand(searchCmd, contextCmd)
}
