package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/checkpoint"
	"github.com/opencode-ai/opencode/internal/experiment"
	"github.com/spf13/cobra"
)

var checkpointCmd = &cobra.Command{Use: "checkpoint", Short: "Create and restore conflict-safe workspace checkpoints"}
var checkpointCreateCmd = &cobra.Command{Use: "create <id> <paths...>", Args: cobra.MinimumNArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	sessionID, _ := cmd.Flags().GetString("session-id")
	goalID, _ := cmd.Flags().GetString("goal-id")
	taskID, _ := cmd.Flags().GetString("task-id")
	agentID, _ := cmd.Flags().GetString("agent-id")
	verification, _ := cmd.Flags().GetStringSlice("verification")
	manifest, err := checkpoint.NewManager(root).CreateWithMetadata(args[0], args[1:], checkpoint.Metadata{
		SessionID:    sessionID,
		GoalID:       goalID,
		TaskID:       taskID,
		AgentID:      agentID,
		Verification: verification,
	})
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var checkpointListCmd = &cobra.Command{Use: "list", RunE: func(_ *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	manifests, err := checkpoint.NewManager(root).List()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(manifests, "", "  ")
	fmt.Println(string(data))
	return nil
}}

func runCheckpointRestore(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	approve, _ := cmd.Flags().GetBool("approve")
	if !approve {
		plan, planErr := checkpoint.NewManager(root).Plan(args[0])
		if planErr != nil {
			return planErr
		}
		data, _ := json.MarshalIndent(plan, "", "  ")
		fmt.Println(string(data))
		return fmt.Errorf("restore requires --approve after reviewing the plan")
	}
	return checkpoint.NewManager(root).RestoreWithApproval(args[0], true)
}

var checkpointRestoreCmd = &cobra.Command{Use: "restore <id>", Args: cobra.ExactArgs(1), RunE: runCheckpointRestore}
var rewindCmd = &cobra.Command{Use: "rewind <checkpoint-id>", Short: "Plan or restore a checkpoint after explicit approval", Args: cobra.ExactArgs(1), RunE: runCheckpointRestore}
var forkCmd = &cobra.Command{Use: "fork <experiment-id>", Short: "Fork the workspace into an isolated Git experiment while preserving parent changes", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	record, err := experiment.NewManager(root).StartFork(cmd.Context(), root, args[0])
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(record, "", "  ")
	fmt.Println(string(data))
	return nil
}}

func init() {
	checkpointCmd.AddCommand(checkpointCreateCmd, checkpointListCmd, checkpointRestoreCmd)
	checkpointCreateCmd.Flags().String("session-id", "", "Associate the checkpoint with a session")
	checkpointCreateCmd.Flags().String("goal-id", "", "Associate the checkpoint with a goal")
	checkpointCreateCmd.Flags().String("task-id", "", "Associate the checkpoint with a task")
	checkpointCreateCmd.Flags().String("agent-id", "", "Associate the checkpoint with an agent")
	checkpointCreateCmd.Flags().StringSlice("verification", nil, "Persist a verification reference (repeatable)")
	checkpointRestoreCmd.Flags().Bool("approve", false, "Approve the displayed restore plan")
	rewindCmd.Flags().Bool("approve", false, "Approve the displayed restore plan")
	rootCmd.AddCommand(checkpointCmd, rewindCmd, forkCmd)
}
