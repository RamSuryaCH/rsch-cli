package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/spf13/cobra"
)

type teamResource struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
	Files     int    `json:"files"`
}

var teamCmd = &cobra.Command{Use: "team", Short: "Inspect shared team and organization RSCH resources", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	resources := []teamResource{}
	for _, name := range []string{"agents", "skills", "steering", "hooks", "policies", "specs", "memory"} {
		path := filepath.Join(root, ".rsch", name)
		count, available := countTeamFiles(path)
		resources = append(resources, teamResource{Name: name, Path: filepath.ToSlash(filepath.Join(".rsch", name)), Available: available, Files: count})
	}
	governance := config.GovernanceConfig{}
	if loaded := config.Get(); loaded != nil {
		governance = loaded.Governance
	}
	result := map[string]any{"root": root, "resources": resources, "governance": governance}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("team resources under %s/.rsch\n", root)
	for _, resource := range resources {
		state := "missing"
		if resource.Available {
			state = "available"
		}
		fmt.Printf("%s: %s (%d files)\n", resource.Name, state, resource.Files)
	}
	fmt.Printf("approved models: %d\napproved plugins: %d\napproved MCP servers: %d\n", len(governance.ApprovedModels), len(governance.ApprovedPlugins), len(governance.ApprovedMCPServers))
	return nil
}}

func countTeamFiles(root string) (int, bool) {
	count := 0
	available := false
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		available = true
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	return count, available
}

func init() {
	teamCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(teamCmd)
}
