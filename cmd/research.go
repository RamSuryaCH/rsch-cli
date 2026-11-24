package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/opencode-ai/opencode/internal/research"
	"github.com/spf13/cobra"
)

var researchCmd = &cobra.Command{Use: "research", Short: "Manage sources, evidence, and traceable claims"}
var researchHealthCmd = &cobra.Command{Use: "health", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	status := store.Health()
	if err := printResearch(cmd, status); err != nil {
		return err
	}
	if !status.Healthy {
		return fmt.Errorf("research provider is unhealthy: %s", status.Reason)
	}
	return nil
}}
var researchSourceCmd = &cobra.Command{Use: "source", Short: "Manage research sources"}
var researchSourceAddCmd = &cobra.Command{Use: "add <uri>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	title, _ := cmd.Flags().GetString("title")
	location, _ := cmd.Flags().GetString("location")
	publisher, _ := cmd.Flags().GetString("publisher")
	source, err := store.AddSource(research.Source{URI: args[0], Title: title, Location: location, Publisher: publisher, Kind: "web"})
	if err != nil {
		return err
	}
	fmt.Println(source.ID)
	return nil
}}
var researchSourcesCmd = &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	items, err := store.Sources()
	if err != nil {
		return err
	}
	return printResearch(cmd, items)
}}

var researchEvidenceCmd = &cobra.Command{Use: "evidence", Short: "Manage source-backed evidence"}
var researchEvidenceAddCmd = &cobra.Command{Use: "add <source-id> <quote>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	location, _ := cmd.Flags().GetString("location")
	supports, _ := cmd.Flags().GetBool("supports")
	item, err := store.AddEvidence(research.Evidence{SourceID: args[0], Quote: args[1], Location: location, Supports: supports})
	if err != nil {
		return err
	}
	fmt.Println(item.ID)
	return nil
}}
var researchEvidenceListCmd = &cobra.Command{Use: "list [source-id]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	sourceID := ""
	if len(args) == 1 {
		sourceID = args[0]
	}
	items, err := store.Evidence(sourceID)
	if err != nil {
		return err
	}
	return printResearch(cmd, items)
}}

var researchClaimCmd = &cobra.Command{Use: "claim", Short: "Manage traceable claims"}
var researchClaimAddCmd = &cobra.Command{Use: "add <text>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	evidence, _ := cmd.Flags().GetStringSlice("evidence")
	confidence, _ := cmd.Flags().GetFloat64("confidence")
	claim, err := store.AddClaim(research.Claim{Text: args[0], EvidenceIDs: evidence, Confidence: confidence})
	if err != nil {
		return err
	}
	fmt.Printf("%s [%s]\n", claim.ID, claim.Status)
	return nil
}}
var researchClaimListCmd = &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	items, err := store.Claims()
	if err != nil {
		return err
	}
	return printResearch(cmd, items)
}}
var researchSearchCmd = &cobra.Command{Use: "search <query>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	store, err := researchStore()
	if err != nil {
		return err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	items, err := store.Search(args[0], limit)
	if err != nil {
		return err
	}
	return printResearch(cmd, items)
}}

func researchStore() (*research.Store, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return research.New(root)
}

func printResearch(cmd *cobra.Command, value any) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	data, _ := json.Marshal(value)
	text := strings.TrimSpace(string(data))
	if text == "null" || text == "[]" {
		fmt.Println("no research records")
		return nil
	}
	fmt.Println(text)
	return nil
}

func init() {
	researchSourceAddCmd.Flags().String("title", "", "source title")
	researchSourceAddCmd.Flags().String("publisher", "", "source publisher")
	researchSourceAddCmd.Flags().String("location", "", "page, section, or local location")
	researchEvidenceAddCmd.Flags().String("location", "", "page, section, or local location")
	researchEvidenceAddCmd.Flags().Bool("supports", true, "whether the evidence supports the claim")
	researchClaimAddCmd.Flags().StringSlice("evidence", nil, "evidence ID (repeatable)")
	researchClaimAddCmd.Flags().Float64("confidence", 0.5, "claim confidence from 0 to 1")
	for _, command := range []*cobra.Command{researchHealthCmd, researchSourcesCmd, researchEvidenceListCmd, researchClaimListCmd, researchSearchCmd} {
		command.Flags().Bool("json", false, "Output structured JSON")
	}
	researchSearchCmd.Flags().Int("limit", 20, "maximum claims to return")
	researchSourceCmd.AddCommand(researchSourceAddCmd, researchSourcesCmd)
	researchEvidenceCmd.AddCommand(researchEvidenceAddCmd, researchEvidenceListCmd)
	researchClaimCmd.AddCommand(researchClaimAddCmd, researchClaimListCmd)
	researchCmd.AddCommand(researchHealthCmd, researchSourceCmd, researchEvidenceCmd, researchClaimCmd, researchSearchCmd)
	rootCmd.AddCommand(researchCmd)
}
