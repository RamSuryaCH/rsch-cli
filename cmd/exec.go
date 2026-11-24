package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

// execCmd gives automation a dedicated entry point that can never fall back
// to the interactive TUI. The root prompt flags remain supported for backward
// compatibility.
var execCmd = &cobra.Command{
	Use:   "exec [prompt]",
	Short: "Run one prompt non-interactively with stable machine output",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		flagPrompt, _ := cmd.Flags().GetString("prompt")
		if cmd.Flags().Changed("prompt") && len(args) > 0 {
			return newUsageError("provide the prompt with either --prompt or one positional argument, not both")
		}
		if len(args) == 1 {
			flagPrompt = strings.TrimSpace(args[0])
		}
		if flagPrompt == "" {
			noInput, _ := cmd.Flags().GetBool("no-input")
			if noInput {
				return newUsageError("no prompt provided while --no-input is enabled")
			}
			stdinPrompt, redirected, err := readPromptInput(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if !redirected {
				return newUsageError("no prompt provided; pass one argument, --prompt, or redirected stdin")
			}
			if stdinPrompt == "" {
				return newUsageError("no prompt provided on non-interactive stdin")
			}
			flagPrompt = stdinPrompt
		}
		if err := cmd.Flags().Set("prompt", flagPrompt); err != nil {
			return err
		}
		return rootCmd.RunE(cmd, nil)
	},
}

func init() {
	execCmd.Flags().Bool("no-input", false, "Do not read stdin; fail if no prompt is supplied")
	rootCmd.AddCommand(execCmd)
}
