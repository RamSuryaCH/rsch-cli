package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/feature"
	"github.com/spf13/cobra"
)

var featureCmd = &cobra.Command{Use: "feature", Short: "Inspect the evidence-based product feature registry"}
var featureListCmd = &cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	registry, err := feature.Load(root)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(feature.Sort(registry), "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}}
var featureValidateCmd = &cobra.Command{Use: "validate", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	registry, err := feature.Load(root)
	if err != nil {
		return err
	}
	if err := feature.Validate(registry); err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "feature registry: valid")
	return err
}}
var featureInitCmd = &cobra.Command{Use: "init", Short: "Create an empty feature registry", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(feature.Path(root)); statErr == nil {
		return fmt.Errorf("feature registry already exists")
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := feature.Save(root, feature.Registry{Version: 1, Features: []feature.Entry{}}); err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), feature.Path(root))
	return err
}}

func init() {
	featureCmd.AddCommand(featureListCmd, featureValidateCmd, featureInitCmd)
	rootCmd.AddCommand(featureCmd)
}
