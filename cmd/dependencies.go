package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/dependencies"
	"github.com/spf13/cobra"
)

var dependenciesCmd = &cobra.Command{
	Use:   "dependencies",
	Short: "Inspect repository dependency manifests and graphs",
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := os.Getwd()
		if err != nil {
			return err
		}
		report, err := dependencies.Scan(context.Background(), root)
		if err != nil {
			return err
		}
		if err := recordEvidence(root, "dependencies", "rsch dependencies", "observed", fmt.Sprintf("found %d dependencies and %d findings", len(report.Dependencies), len(report.Findings)), "", report); err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(data))
			return nil
		}
		if len(report.Dependencies) == 0 {
			fmt.Println("no supported dependencies found")
			return nil
		}
		if report.AffectedAvailable {
			for _, packageInfo := range report.Packages {
				if packageInfo.Affected {
					fmt.Printf("affected package: %s (%s) files=%d\n", packageInfo.Path, packageInfo.Manager, len(packageInfo.AffectedFiles))
				}
			}
			for _, plan := range report.ExecutionPlans {
				status := "unavailable"
				if plan.Available {
					status = "available"
				}
				fmt.Printf("execution plan: %s (%s) [%s] dir=%s\n", plan.PackagePath, plan.Manager, status, plan.Directory)
				for _, command := range plan.Commands {
					fmt.Printf("  planned: %s\n", strings.Join(command, " "))
				}
				if plan.Reason != "" {
					fmt.Printf("  reason: %s\n", plan.Reason)
				}
			}
		} else {
			fmt.Println("affected package analysis: unavailable")
		}
		for _, item := range report.Dependencies {
			scope := item.Scope
			if scope == "" {
				scope = "runtime"
			}
			fmt.Printf("%s %s [%s/%s] source=%s\n", item.Name, item.Version, item.Manager, scope, item.SourceFile)
		}
		for _, finding := range report.Findings {
			label := finding.Kind
			if finding.Name != "" {
				label += " " + finding.Name
			}
			if finding.Current != "" || finding.Available != "" {
				label += fmt.Sprintf(" (%s -> %s)", finding.Current, finding.Available)
			}
			fmt.Printf("finding: %s", label)
			if finding.Tool != "" {
				fmt.Printf(" [%s]", finding.Tool)
			}
			fmt.Println()
		}
		for _, warning := range report.Warnings {
			fmt.Printf("warning: %s\n", warning)
		}
		return nil
	},
}

func init() {
	dependenciesCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(dependenciesCmd)
}
