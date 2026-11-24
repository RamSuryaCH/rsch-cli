package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencode-ai/opencode/internal/intelligence"
	"github.com/opencode-ai/opencode/internal/visual"
	"github.com/spf13/cobra"
)

var architectureCmd = &cobra.Command{Use: "architecture", Short: "Build and inspect the repository architecture graph", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	index, err := intelligence.New(root)
	if err != nil {
		return err
	}
	graph, err := index.Architecture(cmd.Context())
	if err != nil {
		return err
	}
	_ = intelligence.SaveArchitecture(root, graph)
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(graph, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("nodes: %d\nedges: %d\n", len(graph.Nodes), len(graph.Edges))
	}
	return nil
}}

var architectureRenderCmd = &cobra.Command{Use: "render", Short: "Render a visual architecture diagram from the repository graph", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	index, err := intelligence.New(root)
	if err != nil {
		return err
	}
	graph, err := index.Architecture(cmd.Context())
	if err != nil {
		return err
	}
	if err := intelligence.SaveArchitecture(root, graph); err != nil {
		return err
	}
	report, err := visual.RenderArchitecture(root, graph)
	if err != nil {
		return err
	}
	if err := recordEvidence(root, "visual", "rsch architecture render", "observed", fmt.Sprintf("rendered architecture diagram with %dx%d pixels", report.Width, report.Height), "", report); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("%s %dx%d (%s, %d bytes)\n", report.Path, report.Width, report.Height, report.Format, report.Bytes)
	}
	return nil
}}

var impactCmd = &cobra.Command{Use: "impact <changed-paths...>", Short: "Analyze dependency and test impact of changed paths", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	index, err := intelligence.New(root)
	if err != nil {
		return err
	}
	impact, err := index.Impact(context.Background(), args)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(impact, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Printf("direct files: %d\nindirect files: %d\ntests: %d\nAPIs: %d\ndatabase entities: %d\nrisk: %s\n", len(impact.DirectFiles), len(impact.IndirectFiles), len(impact.Tests), len(impact.APIs), len(impact.DatabaseEntities), impact.Risk)
	}
	return nil
}}

func init() {
	architectureCmd.Flags().Bool("json", false, "Output structured JSON")
	impactCmd.Flags().Bool("json", false, "Output structured JSON")
	architectureRenderCmd.Flags().Bool("json", false, "Output structured JSON")
	architectureCmd.AddCommand(architectureRenderCmd)
	rootCmd.AddCommand(architectureCmd, impactCmd)
}
