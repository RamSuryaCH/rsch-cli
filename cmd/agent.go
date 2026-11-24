package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/customagent"
	"github.com/opencode-ai/opencode/internal/project"
	"github.com/opencode-ai/opencode/internal/risk"
	"github.com/opencode-ai/opencode/internal/routing"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/spf13/cobra"
)

var customAgentCmd = &cobra.Command{Use: "agent", Short: "Manage repository-defined agents"}

func customAgentManager() (customagent.Manager, error) {
	root, err := os.Getwd()
	if err != nil {
		return customagent.Manager{}, err
	}
	return customagent.New(root)
}

func redactedAgent(entry customagent.Entry) customagent.Entry {
	entry.Definition.Instructions = security.RedactText(entry.Definition.Instructions)
	entry.Definition.Role = security.RedactText(entry.Definition.Role)
	return entry
}

var customAgentListCmd = &cobra.Command{Use: "list", Short: "List repository-defined agents", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	manager, err := customAgentManager()
	if err != nil {
		return err
	}
	entries, err := manager.List()
	if err != nil {
		return err
	}
	for i := range entries {
		entries[i] = redactedAgent(entries[i])
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	for _, entry := range entries {
		fmt.Printf("%s\t%s\n", entry.Name, entry.Definition.Role)
	}
	return nil
}}

var customAgentInspectCmd = &cobra.Command{Use: "inspect <name>", Args: cobra.ExactArgs(1), Short: "Inspect a repository-defined agent", RunE: func(cmd *cobra.Command, args []string) error {
	manager, err := customAgentManager()
	if err != nil {
		return err
	}
	entry, err := manager.Inspect(args[0])
	if err != nil {
		return err
	}
	entry = redactedAgent(entry)
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(entry, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("%s\nrole: %s\nversion: %s\nmodel: %s\nprovider: %s\nrisk: %s\n", entry.Name, entry.Definition.Role, entry.Definition.Version, entry.Definition.Model, entry.Definition.Provider, entry.Definition.Risk)
	return nil
}}

var customAgentCreateCmd = &cobra.Command{Use: "create <name>", Args: cobra.ExactArgs(1), Short: "Create a repository-defined agent", RunE: func(cmd *cobra.Command, args []string) error {
	manager, err := customAgentManager()
	if err != nil {
		return err
	}
	role, _ := cmd.Flags().GetString("role")
	instructions, _ := cmd.Flags().GetString("instructions")
	version, _ := cmd.Flags().GetString("version")
	model, _ := cmd.Flags().GetString("model")
	provider, _ := cmd.Flags().GetString("provider")
	risk, _ := cmd.Flags().GetString("risk")
	skills, _ := cmd.Flags().GetStringSlice("skill")
	tools, _ := cmd.Flags().GetStringSlice("tool")
	permissions, _ := cmd.Flags().GetStringSlice("permission")
	budget, _ := cmd.Flags().GetInt64("budget")
	definition := project.Manifest{ID: args[0], Name: args[0], Version: version, Role: role, Instructions: instructions, Model: model, Provider: provider, Risk: risk, Skills: skills, Tools: tools, Permissions: permissions, Budget: budget}
	path, err := manager.Create(args[0], definition)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}}

var customAgentRemoveCmd = &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), Short: "Remove a repository-defined agent", RunE: func(_ *cobra.Command, args []string) error {
	manager, err := customAgentManager()
	if err != nil {
		return err
	}
	if err := manager.Remove(args[0]); err != nil {
		return err
	}
	fmt.Printf("agent %q removed\n", args[0])
	return nil
}}

var customAgentRouteCmd = &cobra.Command{Use: "route <prompt>", Args: cobra.ExactArgs(1), Short: "Select a declarative agent for a task", RunE: func(cmd *cobra.Command, args []string) error {
	manager, err := customAgentManager()
	if err != nil {
		return err
	}
	entries, err := manager.List()
	if err != nil {
		return err
	}
	complexity, _ := cmd.Flags().GetInt("complexity")
	riskLevel, _ := cmd.Flags().GetString("risk")
	className, _ := cmd.Flags().GetString("class")
	capabilities, _ := cmd.Flags().GetStringSlice("capability")
	skills, _ := cmd.Flags().GetStringSlice("skill")
	tools, _ := cmd.Flags().GetStringSlice("tool")
	decision, err := routing.NewAgentRouter(entries).Route(routing.AgentRequest{
		Prompt:               args[0],
		RequiredCapabilities: capabilities,
		RequiredSkills:       skills,
		RequiredTools:        tools,
		Risk:                 risk.Level(riskLevel),
		Complexity:           complexity,
		Override:             routing.TaskClass(className),
	})
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(decision, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("agent: %s\nclass: %s\nmode: %s\nmodel: %s\nprovider: %s\n", decision.Agent, decision.Class, decision.Execution.Mode, decision.Model, decision.Provider)
	for _, reason := range decision.Reasons {
		fmt.Printf("reason: %s\n", reason)
	}
	return nil
}}

func init() {
	for _, command := range []*cobra.Command{customAgentListCmd, customAgentInspectCmd, customAgentRouteCmd} {
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	customAgentCreateCmd.Flags().String("role", "", "Agent role (required)")
	customAgentCreateCmd.Flags().String("instructions", "", "Agent instructions")
	customAgentCreateCmd.Flags().String("version", "1", "Agent manifest version")
	customAgentCreateCmd.Flags().String("model", "", "Preferred model")
	customAgentCreateCmd.Flags().String("provider", "", "Preferred provider")
	customAgentCreateCmd.Flags().String("risk", "", "Risk profile")
	customAgentCreateCmd.Flags().StringSlice("skill", nil, "Skill name (repeatable)")
	customAgentCreateCmd.Flags().StringSlice("tool", nil, "Tool name (repeatable)")
	customAgentCreateCmd.Flags().StringSlice("permission", nil, "Permission (repeatable)")
	customAgentCreateCmd.Flags().Int64("budget", 0, "Token/cost budget metadata")
	customAgentRouteCmd.Flags().StringSlice("capability", nil, "Required capability (repeatable)")
	customAgentRouteCmd.Flags().StringSlice("skill", nil, "Required skill (repeatable)")
	customAgentRouteCmd.Flags().StringSlice("tool", nil, "Required tool (repeatable)")
	customAgentRouteCmd.Flags().String("risk", "", "Minimum risk profile")
	customAgentRouteCmd.Flags().Int("complexity", 0, "Task complexity from 0 to 10")
	customAgentRouteCmd.Flags().String("class", "", "Explicit task class override")
	customAgentCmd.AddCommand(customAgentListCmd, customAgentInspectCmd, customAgentCreateCmd, customAgentRemoveCmd, customAgentRouteCmd)
	rootCmd.AddCommand(customAgentCmd)
}
