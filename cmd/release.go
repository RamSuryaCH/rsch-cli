package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/opencode-ai/opencode/internal/release"
	"github.com/spf13/cobra"
)

var releaseCmd = &cobra.Command{Use: "release", Short: "Validate and inspect the RSCH release matrix"}
var releaseTargetsCmd = &cobra.Command{Use: "targets", Short: "List evidence-backed cross-platform release targets", RunE: func(_ *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	targets, err := release.ValidatedTargets(root)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("no validated release targets")
			return nil
		}
		return err
	}
	if len(targets) == 0 {
		fmt.Println("no validated release targets")
		return nil
	}
	for _, target := range targets {
		fmt.Printf("%s/%s\n", target.GOOS, target.GOARCH)
	}
	return nil
}}
var releaseMatrixCmd = &cobra.Command{Use: "matrix", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	matrix, err := release.Load(root)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "no release evidence recorded")
			return nil
		}
		return fmt.Errorf("load release matrix: %w", err)
	}
	data, _ := json.MarshalIndent(matrix, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}}
var releaseCheckCmd = &cobra.Command{Use: "check <goos> <goarch>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	target := release.Target{GOOS: args[0], GOARCH: args[1]}
	if err := release.ValidateTarget(target); err != nil {
		return err
	}
	matrix, err := release.Load(root)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("load release matrix: %w", err)
		}
		matrix = release.Matrix{Version: 1}
	}
	evidence := release.Check(cmd.Context(), root, target)
	matrix.Evidence = append(matrix.Evidence, evidence)
	if err := release.Save(root, matrix); err != nil {
		return err
	}
	status := "observed"
	if !evidence.Built {
		status = "failed"
	}
	if err := recordEvidence(root, "release", "rsch release check", status, fmt.Sprintf("checked release target %s/%s", target.GOOS, target.GOARCH), evidence.Output, evidence); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(evidence, "", "  ")
	fmt.Println(string(data))
	if !evidence.Built {
		return fmt.Errorf("release build failed")
	}
	if target.GOOS == runtime.GOOS && target.GOARCH == runtime.GOARCH {
		return nil
	}
	return nil
}}

var releaseCheckAllCmd = &cobra.Command{Use: "check-all", Short: "Build and record evidence for every declared release target", RunE: func(cmd *cobra.Command, _ []string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	matrix := release.CheckAll(cmd.Context(), root)
	if err := release.Save(root, matrix); err != nil {
		return err
	}
	status := "observed"
	for _, evidence := range matrix.Evidence {
		if !evidence.Built {
			status = "failed"
			break
		}
	}
	if err := recordEvidence(root, "release", "rsch release check-all", status, fmt.Sprintf("checked %d release targets", len(matrix.Evidence)), "", matrix); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(matrix, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	for _, evidence := range matrix.Evidence {
		if !evidence.Built {
			return fmt.Errorf("release target %s/%s did not validate", evidence.Target.GOOS, evidence.Target.GOARCH)
		}
	}
	return nil
}}

func init() {
	releaseCmd.AddCommand(releaseTargetsCmd, releaseMatrixCmd, releaseCheckCmd, releaseCheckAllCmd)
	rootCmd.AddCommand(releaseCmd)
}
