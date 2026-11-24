package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/opencode-ai/opencode/internal/requirementaudit"
	"github.com/opencode-ai/opencode/internal/spec"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

var specCmd = &cobra.Command{Use: "spec", Short: "Create, validate, trace, and inspect repository specifications"}
var specNewCmd = &cobra.Command{Use: "new <feature>", Short: "Create a repository specification", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	path, err := spec.NewManager(mustCwd()).New(args[0])
	if err == nil {
		fmt.Println(path)
	}
	return err
}}
var specShowCmd = &cobra.Command{Use: "show <feature>", Short: "Show all files in a repository specification", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	files, err := spec.NewManager(mustCwd()).Show(args[0])
	if err != nil {
		return err
	}
	for _, name := range spec.SortFiles(spec.RequiredFiles()) {
		fmt.Printf("--- %s ---\n%s\n", name, files[name])
	}
	return nil
}}
var specValidateCmd = &cobra.Command{Use: "validate <feature>", Short: "Validate a repository specification", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	if err := spec.NewManager(mustCwd()).Validate(args[0]); err != nil {
		return err
	}
	fmt.Println("specification valid")
	return nil
}}
var specRunCmd = &cobra.Command{Use: "run <feature>", Short: "Run the executable plan for a specification", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	manager := spec.NewManager(mustCwd())
	if err := manager.Validate(args[0]); err != nil {
		return err
	}
	results, err := manager.RunPlan(cmd.Context(), mustCwd(), args[0])
	data, _ := json.MarshalIndent(results, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}}
var specTaskCmd = &cobra.Command{Use: "task", Short: "Manage executable specification tasks"}
var specTaskAddCmd = &cobra.Command{Use: "add <feature> <id> <command> [args...]", Short: "Add an executable task to a specification", Args: cobra.MinimumNArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
	manager := spec.NewManager(mustCwd())
	if err := manager.Validate(args[0]); err != nil {
		return err
	}
	depends, _ := cmd.Flags().GetStringSlice("depends")
	retry, _ := cmd.Flags().GetInt("retry")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	return manager.AddPlanTask(args[0], spec.PlanTask{ID: args[1], Title: args[1], Command: args[2:], Dependencies: depends, RetryLimit: retry, Timeout: timeout})
}}
var specTraceCmd = &cobra.Command{Use: "trace <feature> [paths...]", Short: "Capture file traceability for a specification", Args: cobra.MinimumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	manager := spec.NewManager(mustCwd())
	trace, err := manager.Capture(mustCwd(), args[0], args[1:])
	if err != nil {
		return err
	}
	fmt.Printf("captured traceability for %d files\n", len(trace.Files))
	return nil
}}
var specDriftCmd = &cobra.Command{Use: "drift <feature>", Short: "Check a specification for file drift", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	manager := spec.NewManager(mustCwd())
	report, err := manager.CheckDrift(mustCwd(), args[0])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
	if report.HasDrift() {
		return fmt.Errorf("specification drift detected: %d changed, %d missing, %d added", len(report.Changed), len(report.Missing), len(report.Added))
	}
	return nil
}}
var specArchiveCmd = &cobra.Command{Use: "archive <feature>", Short: "Archive a repository specification", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
	path, err := spec.NewManager(mustCwd()).Archive(args[0])
	if err == nil {
		fmt.Println(path)
	}
	return err
}}
var specAuditCmd = &cobra.Command{Use: "audit <master-prompt>", Short: "Audit numbered master-specification sections against registry evidence", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root := mustCwd()
	report, err := requirementaudit.Run(args[0], root)
	if err != nil {
		return err
	}
	if err := requirementaudit.Save(root, report); err != nil {
		return err
	}
	if err := recordEvidence(root, "requirements", "rsch spec audit", "observed", fmt.Sprintf("audited %d master-specification sections", report.TotalSections), "", report); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	} else {
		fmt.Printf("sections: %d\ntracked: %d\nevidence-backed: %d\nuntracked: %d\n", report.TotalSections, report.TrackedSections, report.EvidenceSections, report.UntrackedSections)
	}
	return nil
}}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Clean(".")
	}
	return cwd
}
func init() {
	specTaskAddCmd.Flags().StringSlice("depends", nil, "task IDs that must pass first")
	specTaskAddCmd.Flags().Int("retry", 0, "maximum retries")
	specTaskAddCmd.Flags().Duration("timeout", 0, "task timeout")
	specTaskCmd.AddCommand(specTaskAddCmd)
	specAuditCmd.Flags().Bool("json", false, "Output structured JSON")
	specCmd.AddCommand(specNewCmd, specShowCmd, specValidateCmd, specRunCmd, specTraceCmd, specDriftCmd, specTaskCmd, specArchiveCmd, specAuditCmd)
	rootCmd.AddCommand(specCmd)
}
