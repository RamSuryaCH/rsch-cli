package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/spf13/cobra"
)

const instructionPrecedence = "repository root to working directory; later files take precedence"

type instructionSelection struct {
	Order     int    `json:"order"`
	Path      string `json:"path"`
	Directory string `json:"directory"`
	Override  bool   `json:"override"`
}

type instructionExplanation struct {
	WorkingDirectory string                 `json:"workingDirectory"`
	Precedence       string                 `json:"precedence"`
	Instructions     []instructionSelection `json:"instructions"`
}

func explainInstructions(workDir string) (instructionExplanation, error) {
	files, err := prompt.DiscoverInstructionFiles(workDir)
	if err != nil {
		return instructionExplanation{}, err
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return instructionExplanation{}, fmt.Errorf("resolve instruction working directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return instructionExplanation{}, fmt.Errorf("resolve instruction working directory symlinks: %w", err)
	}

	report := instructionExplanation{
		WorkingDirectory: filepath.Clean(canonical),
		Precedence:       instructionPrecedence,
		Instructions:     make([]instructionSelection, 0, len(files)),
	}
	for index, path := range files {
		report.Instructions = append(report.Instructions, instructionSelection{
			Order:     index + 1,
			Path:      path,
			Directory: filepath.Dir(path),
			Override:  filepath.Base(path) == "AGENTS.override.md",
		})
	}
	return report, nil
}

func writeInstructionExplanation(w io.Writer, report instructionExplanation, jsonOutput bool) error {
	if jsonOutput {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	if _, err := fmt.Fprintf(w, "instruction precedence: %s\n", report.Precedence); err != nil {
		return err
	}
	if len(report.Instructions) == 0 {
		_, err := fmt.Fprintf(w, "no AGENTS.md instructions apply to %s\n", report.WorkingDirectory)
		return err
	}
	for _, instruction := range report.Instructions {
		kind := "AGENTS.md"
		if instruction.Override {
			kind = "AGENTS.override.md"
		}
		if _, err := fmt.Fprintf(w, "%d. [%s] %s\n", instruction.Order, kind, instruction.Path); err != nil {
			return err
		}
	}
	return nil
}

var instructionsCmd = &cobra.Command{
	Use:   "instructions",
	Short: "Inspect resolved repository instructions",
	Args:  cobra.NoArgs,
}

var instructionsExplainCmd = &cobra.Command{
	Use:   "explain [directory]",
	Short: "Explain the AGENTS.md files that apply to a directory",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		workDir := "."
		if len(args) == 1 {
			workDir = args[0]
		}
		report, err := explainInstructions(workDir)
		if err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		return writeInstructionExplanation(cmd.OutOrStdout(), report, jsonOutput)
	},
}

func init() {
	instructionsExplainCmd.Flags().Bool("json", false, "Output structured JSON")
	instructionsCmd.AddCommand(instructionsExplainCmd)
	rootCmd.AddCommand(instructionsCmd)
}
