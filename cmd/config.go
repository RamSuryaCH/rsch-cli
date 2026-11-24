package cmd

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{Use: "config", Short: "Inspect and validate RSCH configuration"}
var configShowCmd = &cobra.Command{Use: "show", RunE: func(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	value, err := config.LoadHierarchy(config.HierarchyPaths(cwd)...)
	if err != nil {
		return fmt.Errorf("invalid configuration hierarchy: %w", err)
	}
	redactConfig(value)
	return writeCLIJSON(cmd.OutOrStdout(), value)
}}
var configValidateCmd = &cobra.Command{Use: "validate", RunE: func(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := config.Load(cwd, false); err != nil {
		return fmt.Errorf("configuration invalid: %w", err)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
	return err
}}

var configHierarchyCmd = &cobra.Command{Use: "hierarchy", Short: "Show merged global, organization, repository, directory, session, and command configuration", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	paths := config.HierarchyPaths(root)
	value, err := config.LoadHierarchy(paths...)
	if err != nil {
		return err
	}
	redactConfig(value)
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return writeCLIJSON(cmd.OutOrStdout(), map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH, "paths": paths, "config": value})
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "configuration precedence: global -> organization -> repository -> directory -> session -> command"); err != nil {
		return err
	}
	for _, path := range paths {
		if _, statErr := os.Stat(path); statErr == nil {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "loaded: %s\n", path); err != nil {
				return err
			}
		}
	}
	return writeCLIJSON(cmd.OutOrStdout(), value)
}}

func redactConfig(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			lower := strings.ToLower(key)
			if sensitiveConfigKey(lower) {
				typed[key] = "[redacted]"
			} else if lower == "env" {
				redactConfigEnvironment(item)
			} else if text, ok := item.(string); ok {
				typed[key] = security.RedactText(text)
			} else {
				redactConfig(item)
			}
		}
	case []any:
		for i, item := range typed {
			if text, ok := item.(string); ok {
				typed[i] = security.RedactText(text)
			} else {
				redactConfig(item)
			}
		}
	}
}

func sensitiveConfigKey(key string) bool {
	for _, sensitive := range []string{"key", "token", "password", "secret", "authorization", "cookie", "credential", "passwd", "passphrase"} {
		if strings.Contains(key, sensitive) {
			return true
		}
	}
	return false
}

// Environment values can carry credentials under arbitrary variable names.
// Preserve names for inspection without disclosing explicitly assigned values.
func redactConfigEnvironment(value any) {
	redactEntry := func(entry string) string {
		if name, _, assigned := strings.Cut(entry, "="); assigned {
			return name + "=[redacted]"
		}
		return security.RedactText(entry)
	}
	switch typed := value.(type) {
	case []any:
		for i, item := range typed {
			if entry, ok := item.(string); ok {
				typed[i] = redactEntry(entry)
			} else {
				redactConfig(item)
			}
		}
	case []string:
		for i, entry := range typed {
			typed[i] = redactEntry(entry)
		}
	case map[string]any:
		for name := range typed {
			typed[name] = "[redacted]"
		}
	}
}
func init() {
	configHierarchyCmd.Flags().Bool("json", false, "Output structured JSON")
	configCmd.AddCommand(configShowCmd, configValidateCmd, configHierarchyCmd)
	rootCmd.AddCommand(configCmd)
}
