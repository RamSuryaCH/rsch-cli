package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/experiment"
	"github.com/spf13/cobra"
)

var experimentCmd = &cobra.Command{Use: "experiment [name]", Short: "Manage isolated Git experiments", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	loaded, err := loadExperimentConfig(root)
	if err != nil {
		return err
	}
	record, err := experiment.NewManager(root, loaded.Resources.MaxWorktrees).Start(cmd.Context(), root, args[0])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var experimentStartCmd = &cobra.Command{Use: "start <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	loaded, err := loadExperimentConfig(root)
	if err != nil {
		return err
	}
	record, err := experiment.NewManager(root, loaded.Resources.MaxWorktrees).Start(cmd.Context(), root, args[0])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var experimentListCmd = &cobra.Command{Use: "list", RunE: func(_ *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	records, err := experiment.NewManager(root).List()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(records, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var experimentCompareCmd = &cobra.Command{Use: "compare <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	comparison, err := experiment.NewManager(root).Compare(cmd.Context(), args[0])
	if contextErr := cmd.Context().Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		// Preserve the fact that the comparison was attempted even when the
		// host cannot provide Git/worktree evidence. This keeps confidence
		// honest without turning an unavailable dimension into a pass.
		if evidenceErr := recordEvidence(root, "experiment", "rsch experiment compare", "unavailable", fmt.Sprintf("experiment %s comparison unavailable", args[0]), err.Error(), map[string]any{"experimentId": args[0]}); evidenceErr != nil {
			return fmt.Errorf("experiment comparison unavailable: %v; recording evidence: %w", err, evidenceErr)
		}
		return err
	}
	status := "observed"
	if !comparison.Tests.Available || !comparison.Performance.Available || !comparison.Security.Available || !comparison.Complexity.Available {
		status = "unavailable"
	} else if !comparison.Tests.Passed || !comparison.Security.Passed {
		status = "failed"
	}
	if err := recordEvidence(root, "experiment", "rsch experiment compare", status, fmt.Sprintf("compared experiment %s with %d changed files", args[0], len(comparison.ChangedFiles)), "", comparison); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(comparison, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var experimentCloseCmd = &cobra.Command{Use: "close <id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	return experiment.NewManager(root).Close(cmd.Context(), args[0])
}}

func init() {
	experimentCmd.AddCommand(experimentStartCmd, experimentListCmd, experimentCompareCmd, experimentCloseCmd)
	rootCmd.AddCommand(experimentCmd)
}

func loadExperimentConfig(root string) (*config.Config, error) {
	if loaded := config.Get(); loaded != nil {
		return loaded, nil
	}
	return config.Load(root, false)
}
