package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/explain"
	"github.com/opencode-ai/opencode/internal/gitcommit"
	"github.com/opencode-ai/opencode/internal/gitsafety"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/review"
	"github.com/opencode-ai/opencode/internal/worktree"
	"github.com/spf13/cobra"
)

var gitCmd = &cobra.Command{Use: "git", Short: "Inspect and safely manage Git workflow"}
var gitSmartCommitCmd = &cobra.Command{Use: "smart-commit", Aliases: []string{"commit"}, Short: "Preview or explicitly create a conventional commit from staged changes", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	preview, err := gitcommit.PreviewStaged(cmd.Context(), root)
	if err != nil {
		return err
	}
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	message, _ := cmd.Flags().GetString("message")
	if strings.TrimSpace(message) != "" {
		preview.Message = message
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	commit, _ := cmd.Flags().GetBool("commit")
	if !commit {
		if jsonOutput {
			data, _ := json.MarshalIndent(preview, "", "  ")
			fmt.Println(string(data))
		} else {
			fmt.Printf("message: %s\n%s\n", preview.Message, preview.Stat)
			for _, file := range preview.Files {
				fmt.Printf("- %s\n", file)
			}
			fmt.Println("preview only; rerun with --commit to create the commit")
		}
		return nil
	}
	if err := gitcommit.Commit(cmd.Context(), root, preview, message); err != nil {
		return err
	}
	fmt.Printf("created commit: %s\n", preview.Message)
	return nil
}}

var gitStatusCmd = &cobra.Command{Use: "status", Short: "Show Git working-tree status", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	status, err := gitsafety.Inspect(cmd.Context(), root)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if !status.Available {
		fmt.Printf("Git repository: unavailable\nReason: %s\n", status.Reason)
		return nil
	}
	if status.Branch != "" {
		fmt.Printf("branch: %s\n", status.Branch)
	}
	if status.Clean {
		fmt.Println("clean")
		return nil
	}
	for _, entry := range status.Entries {
		fmt.Println(entry)
	}
	return nil
}}
var gitDiffCmd = &cobra.Command{Use: "diff", Short: "Show a bounded Git diff", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := process.RunOutput(cmd.Context(), process.Spec{Argv: []string{"git", "-C", root, "diff", "--", "."}})
	if err != nil {
		return err
	}
	output := result.Output
	if len(output) > 1<<20 {
		output = append(output[:1<<20], []byte("\n[diff truncated]\n")...)
	}
	fmt.Print(string(output))
	return nil
}}
var gitBranchCmd = &cobra.Command{Use: "branch", Short: "List Git branches", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	result, err := process.RunOutput(cmd.Context(), process.Spec{Argv: []string{"git", "-C", root, "branch", "--format=%(refname:short)"}})
	if err != nil {
		return err
	}
	fmt.Print(string(result.Output))
	return nil
}}
var gitWorktreeCmd = &cobra.Command{Use: "worktree", Short: "List isolated Git worktrees", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	items, err := worktree.NewManager(root).List(cmd.Context(), root)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Println(item)
	}
	return nil
}}
var gitReviewCmd = &cobra.Command{Use: "review", Short: "Run read-only Git change review", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := review.Run(cmd.Context(), root)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	if !report.Ready {
		return fmt.Errorf("review requires attention")
	}
	return nil
}}
var gitExplainCmd = &cobra.Command{Use: "explain [path]", Args: cobra.MaximumNArgs(1), Short: "Explain a Git-tracked file using repository evidence", RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	report, err := explain.Run(cmd.Context(), root, target)
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(data))
	return nil
}}
var gitPRCmd = &cobra.Command{Use: "pr", Short: "Preview or explicitly create a pull request through the configured GitHub CLI", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	branchResult, err := process.RunOutput(cmd.Context(), process.Spec{Argv: []string{"git", "-C", root, "branch", "--show-current"}})
	if err != nil {
		return err
	}
	branch := strings.TrimSpace(string(branchResult.Output))
	if branch == "" {
		return fmt.Errorf("cannot create a pull request without a current branch")
	}
	remoteResult, remoteErr := process.RunOutput(cmd.Context(), process.Spec{Argv: []string{"git", "-C", root, "remote", "get-url", "origin"}})
	preview := map[string]any{"branch": branch, "remote": strings.TrimSpace(string(remoteResult.Output)), "remoteConfigured": remoteErr == nil, "created": false}
	create, _ := cmd.Flags().GetBool("create")
	if !create {
		data, _ := json.MarshalIndent(preview, "", "  ")
		fmt.Println(string(data))
		fmt.Println("preview only; rerun with --create to invoke gh pr create --fill")
		return nil
	}
	if !process.Exists("gh") {
		return fmt.Errorf("GitHub CLI (gh) is required for pull request creation")
	}
	result, err := process.RunOutput(cmd.Context(), process.Spec{Argv: []string{"gh", "pr", "create", "--fill"}, Directory: root})
	if err != nil {
		return fmt.Errorf("gh pr create: %w: %s", err, strings.TrimSpace(string(result.Output)))
	}
	preview["created"] = true
	preview["output"] = strings.TrimSpace(string(result.Output))
	data, _ := json.MarshalIndent(preview, "", "  ")
	fmt.Println(string(data))
	return nil
}}

func init() {
	gitSmartCommitCmd.Flags().String("message", "", "Override the inferred conventional commit message")
	gitSmartCommitCmd.Flags().Bool("commit", false, "Actually create the commit after previewing it")
	gitSmartCommitCmd.Flags().Bool("json", false, "Output structured JSON during preview")
	gitCmd.AddCommand(gitSmartCommitCmd)
	gitPRCmd.Flags().Bool("create", false, "Actually invoke gh pr create --fill")
	gitCmd.AddCommand(gitStatusCmd, gitDiffCmd, gitBranchCmd, gitWorktreeCmd, gitReviewCmd, gitExplainCmd, gitPRCmd)
	gitStatusCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(gitCmd)
}
