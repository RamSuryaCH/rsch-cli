package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/steering"
	"github.com/spf13/cobra"
)

var steeringCmd = &cobra.Command{Use: "steering", Short: "Show resolved project steering rules", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	set, err := steering.Load(root)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(set, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(set.Rules) == 0 {
		fmt.Println("no steering rules found")
		return nil
	}
	for _, rule := range set.Rules {
		fmt.Printf("[%s] %s\n", rule.Scope, rule.Path)
	}
	return nil
}}

func init() {
	steeringCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(steeringCmd)
}
