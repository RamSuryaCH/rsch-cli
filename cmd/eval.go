package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/evaluation"
	"github.com/spf13/cobra"
)

var evalCmd = &cobra.Command{Use: "eval", Short: "Run repository-quality checks (not a coding-agent benchmark)", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := evaluation.Run(cmd.Context(), root)
	if err != nil {
		return err
	}
	if saveErr := evaluation.Save(root, report); saveErr != nil {
		return fmt.Errorf("save repository-quality evaluation %s: %w", report.RunID, saveErr)
	}
	status := "observed"
	if !report.RepositoryQualityPassed {
		status = "failed"
	}
	summary := fmt.Sprintf("repository quality passed=%t; coding-agent task evaluated=false", report.RepositoryQualityPassed)
	if evidenceErr := recordEvidence(root, "evaluation", "rsch eval", status, summary, "", report); evidenceErr != nil {
		return evidenceErr
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, check := range report.Checks {
			state := "passed"
			if !check.Passed {
				state = "failed"
			}
			fmt.Printf("%s: %s\n", check.Name, state)
		}
		fmt.Printf("repository quality passed: %t\n", report.RepositoryQualityPassed)
		fmt.Println("coding-agent task evaluated: false")
		fmt.Printf("regression evaluation: unavailable (%s)\n", report.RegressionReason)
		fmt.Printf("run id: %s\nartifact: %s\n", report.RunID, report.Artifact)
	}
	if !report.RepositoryQualityPassed {
		return fmt.Errorf("repository-quality evaluation failed")
	}
	return nil
}}

func init() {
	evalCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(evalCmd)
}
