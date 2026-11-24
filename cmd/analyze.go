package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencode-ai/opencode/internal/visual"
	"github.com/spf13/cobra"
)

var analyzeCmd = &cobra.Command{Use: "analyze <image>", Short: "Analyze a screenshot or image artifact", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	path := args[0]
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	report, err := visual.Analyze(path, root)
	if err != nil {
		return err
	}
	if err := recordEvidence(root, "visual", "rsch analyze", "observed", fmt.Sprintf("analyzed %s", filepath.Base(path)), "", report); err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("%s %dx%d (%s, %d bytes)\n", report.Path, report.Width, report.Height, report.Format, report.Bytes)
	for _, color := range report.Dominant {
		fmt.Printf("color #%02x%02x%02x pixels=%d\n", color.R, color.G, color.B, color.Pixels)
	}
	for _, hint := range report.Hints {
		fmt.Printf("component hint: %s\n", hint)
	}
	return nil
}}

func init() {
	analyzeCmd.Flags().Bool("json", false, "Output structured JSON")
	rootCmd.AddCommand(analyzeCmd)
}
