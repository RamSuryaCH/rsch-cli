package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencode-ai/opencode/internal/audit"
	documentation "github.com/opencode-ai/opencode/internal/documentation"
	"github.com/spf13/cobra"
)

var docsCmd = &cobra.Command{Use: "docs", Short: "Inspect repository documentation and local links", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := documentation.Scan(context.Background(), root)
	if err != nil {
		return err
	}
	if err := recordEvidence(root, "documentation", "rsch docs", "observed", fmt.Sprintf("scanned %d documents and %d broken links", len(report.Documents), len(report.BrokenLinks)), "", report); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("documents: %d\nbroken local links: %d\n", len(report.Documents), len(report.BrokenLinks))
	}
	if len(report.BrokenLinks) > 0 {
		return fmt.Errorf("documentation has broken local links")
	}
	return nil
}}

var docsCheckCmd = &cobra.Command{Use: "check <changed-paths...>", Short: "Check documentation obligations for changed paths", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := documentation.Scan(context.Background(), root)
	if err != nil {
		return err
	}
	impact := documentation.Assess(report, args)
	if err := recordEvidence(root, "documentation", "rsch docs check", "observed", fmt.Sprintf("assessed %d changed paths and %d missing obligations", len(args), len(impact.Missing)), "", impact); err != nil {
		return err
	}
	if log, logErr := audit.New(root); logErr == nil {
		_ = log.Append(audit.Entry{Actor: "cli", Action: "documentation.assess", Resource: filepath.Join(root, ".rsch"), Metadata: map[string]string{"changed": fmt.Sprint(len(args)), "missing": fmt.Sprint(len(impact.Missing))}})
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(impact, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("required: %d\nmissing: %d\n", len(impact.Required), len(impact.Missing))
		for _, area := range impact.Missing {
			fmt.Printf("missing: %s\n", area)
		}
	}
	if len(impact.Missing) > 0 {
		return fmt.Errorf("documentation review required")
	}
	return nil
}}

func init() {
	docsCmd.Flags().Bool("json", false, "Output structured JSON")
	docsCheckCmd.Flags().Bool("json", false, "Output structured JSON")
	docsCmd.AddCommand(docsCheckCmd)
	rootCmd.AddCommand(docsCmd)
}
