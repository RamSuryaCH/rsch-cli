package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/explain"
	"github.com/spf13/cobra"
)

var explainCmd = &cobra.Command{Use: "explain [file <path>|<path>]", Short: "Explain repository behavior using local evidence", Args: cobra.MaximumNArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	target := strings.Join(args, " ")
	report, err := explain.Run(cmd.Context(), root, target)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("target: %s\npurpose: %s\n", report.Target, report.Purpose)
	printExplainList("dependencies", report.Dependencies)
	printExplainList("callers", report.Callers)
	printExplainList("important behavior", report.Behavior)
	printExplainList("potential risks", report.Risks)
	printExplainList("tests", report.Tests)
	printExplainList("recent changes", report.RecentChange)
	return nil
}}

func printExplainList(label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Printf("%s:\n", label)
	for _, value := range values {
		fmt.Printf("- %s\n", value)
	}
}

func init() {
	explainCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(explainCmd)
}
