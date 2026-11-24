package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/confidence"
	"github.com/spf13/cobra"
)

var confidenceCmd = &cobra.Command{
	Use:   "confidence",
	Short: "Calculate completion confidence from observed evidence",
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		report, err := confidence.Build(root)
		if err != nil {
			return err
		}
		if err := confidence.Save(root, report); err != nil {
			return err
		}
		status := "observed"
		if !report.Complete {
			status = "failed"
		}
		if err := recordEvidence(root, "confidence", "rsch confidence", status, "calculated confidence from persisted evidence", "", report); err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		if report.Score == nil {
			fmt.Println("confidence: unavailable (no observed evidence)")
		} else {
			fmt.Printf("confidence: %.0f%% observed-weight=%.0f/%.0f\n", *report.Score, report.ObservedWeight, report.TotalWeight)
		}
		for _, dimension := range report.Dimensions {
			state := "unobserved"
			if dimension.Observed {
				state = "passed"
				if !dimension.Passed {
					state = "failed"
				}
			}
			fmt.Printf("%s: %s (%s)\n", dimension.Name, state, dimension.Reason)
		}
		return nil
	},
}

func init() {
	confidenceCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(confidenceCmd)
}
