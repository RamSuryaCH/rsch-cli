package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/spf13/cobra"
)

var skillCmd = &cobra.Command{Use: "skill", Aliases: []string{"skills"}, Short: "Manage reusable repository skills"}

func skillManager() (skill.Manager, error) {
	root, err := os.Getwd()
	if err != nil {
		return skill.Manager{}, err
	}
	return skill.New(root)
}

var skillListCmd = &cobra.Command{Use: "list", Short: "List installed skills", RunE: func(cmd *cobra.Command, _ []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	entries, err := manager.List()
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
		status := "missing SKILL.md"
		if entry.HasSkillMD {
			status = fmt.Sprintf("%d files", len(entry.Files))
		}
		if entry.Manifest != nil && entry.Manifest.Version != "" {
			status += fmt.Sprintf(" v%s", entry.Manifest.Version)
		}
		if !entry.Enabled {
			status += " (disabled)"
		}
		fmt.Printf("%s\t%s\n", entry.Name, status)
	}
	return nil
}}

var skillCreateCmd = &cobra.Command{Use: "create <name>", Args: cobra.ExactArgs(1), Short: "Create a skill template", RunE: func(_ *cobra.Command, args []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	path, err := manager.Create(args[0])
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}}

var skillInstallCmd = &cobra.Command{Use: "install <name>", Args: cobra.ExactArgs(1), Short: "Install a local skill source", RunE: func(cmd *cobra.Command, args []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	source, _ := cmd.Flags().GetString("source")
	path, err := manager.Install(args[0], source)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}}

var skillComposeCmd = &cobra.Command{Use: "compose <name> [name...]", Args: cobra.MinimumNArgs(1), Short: "Compose skills and their declared dependencies", RunE: func(_ *cobra.Command, args []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	content, err := manager.Compose(args...)
	if err != nil {
		return err
	}
	fmt.Println(content)
	return nil
}}

var skillRemoveCmd = &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), Short: "Remove an installed skill", RunE: func(_ *cobra.Command, args []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	if err := manager.Remove(args[0]); err != nil {
		return err
	}
	fmt.Printf("skill %q removed\n", args[0])
	return nil
}}

var skillInfoCmd = &cobra.Command{Use: "info <name>", Args: cobra.ExactArgs(1), Short: "Inspect a skill and its metadata", RunE: func(cmd *cobra.Command, args []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	entry, err := manager.Info(args[0])
	if err != nil {
		return err
	}
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		data, _ := json.MarshalIndent(entry, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("%s\npath: %s\nenabled: %t\nfiles: %d\nmetadata: %t\n", entry.Name, entry.Path, entry.Enabled, len(entry.Files), entry.Manifest != nil)
	return nil
}}

var skillSearchCmd = &cobra.Command{Use: "search <query>", Args: cobra.ExactArgs(1), Short: "Search skill metadata and bounded instructions", RunE: func(cmd *cobra.Command, args []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	results, err := manager.Search(args[0], limit)
	if err != nil {
		return err
	}
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		data, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	for _, result := range results {
		fmt.Printf("%s\t%d\t%s\n", result.Entry.Name, result.Score, strings.Join(result.Reason, ", "))
	}
	return nil
}}

func setSkillEnabled(enabled bool) *cobra.Command {
	action := "disabled"
	if enabled {
		action = "enabled"
	}
	return &cobra.Command{Use: map[bool]string{true: "enable <name>", false: "disable <name>"}[enabled], Args: cobra.ExactArgs(1), Short: fmt.Sprintf("%s a skill", strings.Title(action)), RunE: func(_ *cobra.Command, args []string) error {
		manager, err := skillManager()
		if err != nil {
			return err
		}
		if err := manager.SetEnabled(args[0], enabled); err != nil {
			return err
		}
		fmt.Printf("skill %q %s\n", args[0], action)
		return nil
	}}
}

var skillAuditCmd = &cobra.Command{Use: "audit", Short: "Audit installed skills without executing them", RunE: func(cmd *cobra.Command, _ []string) error {
	manager, err := skillManager()
	if err != nil {
		return err
	}
	issues, err := manager.Audit()
	if err != nil {
		return err
	}
	if jsonOutput, _ := cmd.Flags().GetBool("json"); jsonOutput {
		data, _ := json.MarshalIndent(issues, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	if len(issues) == 0 {
		fmt.Println("skills audit passed")
		return nil
	}
	for _, issue := range issues {
		fmt.Printf("%s\t%s\t%s\n", issue.Severity, issue.Skill, issue.Message)
	}
	return fmt.Errorf("skills audit found %d issue(s)", len(issues))
}}

var skillDoctorCmd = &cobra.Command{Use: "doctor", Short: "Check skill catalog health", RunE: func(cmd *cobra.Command, args []string) error {
	return skillAuditCmd.RunE(cmd, args)
}}

func init() {
	skillListCmd.Flags().Bool("json", false, "Output structured JSON")
	skillInstallCmd.Flags().String("source", "", "Local skill directory or SKILL.md file")
	skillInfoCmd.Flags().Bool("json", false, "Output structured JSON")
	skillSearchCmd.Flags().Int("limit", 20, "Maximum results")
	skillSearchCmd.Flags().Bool("json", false, "Output structured JSON")
	skillAuditCmd.Flags().Bool("json", false, "Output structured JSON")
	skillCmd.AddCommand(skillListCmd, skillCreateCmd, skillInstallCmd, skillRemoveCmd, skillComposeCmd, skillInfoCmd, skillSearchCmd, setSkillEnabled(true), setSkillEnabled(false), skillAuditCmd, skillDoctorCmd)
	rootCmd.AddCommand(skillCmd)
}
