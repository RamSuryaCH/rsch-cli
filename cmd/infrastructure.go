package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/infrastructure"
	"github.com/spf13/cobra"
)

var infrastructureCmd = &cobra.Command{Use: "infrastructure", Short: "Inspect Docker, Compose, Terraform, and Kubernetes resources", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := infrastructure.Scan(context.Background(), root)
	if err != nil {
		return err
	}
	if err := recordEvidence(root, "infrastructure", "rsch infrastructure", "observed", fmt.Sprintf("found %d infrastructure resources", len(report.Resources)), "", report); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(report.Resources) == 0 {
		fmt.Println("no supported infrastructure resources found")
		return nil
	}
	for _, resource := range report.Resources {
		fmt.Printf("%s %s (%s:%d)\n", resource.Kind, resource.Name, resource.Path, resource.Line)
	}
	return nil
}}

func init() {
	infrastructureCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(infrastructureCmd)
}
