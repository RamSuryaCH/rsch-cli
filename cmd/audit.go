package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/audit"
	"github.com/spf13/cobra"
)

var auditCmd = &cobra.Command{Use: "audit", Short: "Inspect local RSCH audit records", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	log, err := audit.New(root)
	if err != nil {
		return err
	}
	entries, err := log.List(0)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	for _, entry := range entries {
		fmt.Printf("%s actor=%s action=%s decision=%s\n", entry.At.Format("2006-01-02T15:04:05Z07:00"), entry.Actor, entry.Action, entry.Decision)
	}
	if len(entries) == 0 {
		fmt.Println("no audit records")
	}
	return nil
}}

func init() {
	auditCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(auditCmd)
}
