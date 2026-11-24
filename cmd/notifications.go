package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/notification"
	"github.com/spf13/cobra"
)

var notificationsCmd = &cobra.Command{Use: "notifications", Short: "Inspect persisted workflow notifications", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	provider, err := notification.NewFile(root)
	if err != nil {
		return err
	}
	items, err := provider.List(0)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(items) == 0 {
		fmt.Println("no notifications")
		return nil
	}
	for _, item := range items {
		fmt.Printf("%s %s %s\n", item.At.Format("2006-01-02T15:04:05Z07:00"), item.Kind, item.Resource)
	}
	return nil
}}

func init() {
	notificationsCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(notificationsCmd)
}
