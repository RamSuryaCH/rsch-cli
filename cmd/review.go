package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/opencode-ai/opencode/internal/review"
	"github.com/spf13/cobra"
	"os"
)

var reviewCmd = &cobra.Command{Use: "review", Short: "Run independent read-only change review", RunE: func(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	snapshot, _ := cmd.Flags().GetBool("snapshot")
	var report review.Report
	if snapshot {
		report, err = review.RunSnapshot(cmd.Context(), cwd)
	} else {
		report, err = review.Run(cmd.Context(), cwd)
	}
	if err != nil {
		return err
	}
	status := "observed"
	if !report.Ready {
		status = "failed"
	}
	source := "rsch review"
	summary := fmt.Sprintf("reviewed %d changed files", len(report.Changed))
	if snapshot {
		source = "rsch review --snapshot"
		summary = fmt.Sprintf("reviewed %d workspace snapshot files", len(report.Changed))
	}
	if evidenceErr := recordEvidence(cwd, "review", source, status, summary, "", report); evidenceErr != nil {
		return evidenceErr
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("scope: %s\n", report.Scope)
		fmt.Printf("Git available: %t\n", report.GitAvailable)
		fmt.Printf("changed files: %d\n", len(report.Changed))
		fmt.Printf("diff check: %t (checked: %t)\n", report.CleanDiff, report.DiffChecked)
		for _, assessment := range report.Assessments {
			state := "unavailable"
			if assessment.Observed {
				state = "passed"
				if !assessment.Passed {
					state = "failed"
				}
			}
			fmt.Printf("%s: %s (%s)\n", assessment.Name, state, assessment.Reason)
		}
		for _, finding := range report.Findings {
			fmt.Printf("%s %s: %s\n", finding.Severity, finding.Rule, finding.Message)
		}
	}
	if !report.Ready {
		return fmt.Errorf("review requires attention")
	}
	return nil
}}

func init() {
	reviewCmd.Flags().Bool("json", false, "Output structured JSON")
	reviewCmd.Flags().Bool("snapshot", false, "Review the full supported workspace without Git diff claims")
	rootCmd.AddCommand(reviewCmd)
}
