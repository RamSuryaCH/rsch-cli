package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencode-ai/opencode/internal/intelligence"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/spf13/cobra"
)

var securityCmd = &cobra.Command{Use: "security", Short: "Scan the workspace for high-risk local findings", RunE: func(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	index, err := intelligence.New(cwd)
	if err != nil {
		return err
	}
	if err := index.Build(cmd.Context()); err != nil {
		return err
	}
	findings := make([]security.Finding, 0)
	for _, file := range index.Files() {
		items, err := security.ScanFile(filepath.Join(cwd, filepath.FromSlash(file.Path)))
		if err != nil {
			return err
		}
		findings = append(findings, items...)
	}
	externalNames, _ := cmd.Flags().GetStringSlice("external")
	external, err := security.RunExternal(cmd.Context(), cwd, externalNames)
	if err != nil {
		return err
	}
	status := "observed"
	if len(findings) > 0 {
		status = "failed"
	}
	externalUnavailable := false
	externalFailed := false
	for _, result := range external {
		if !result.Available {
			externalUnavailable = true
		}
		if result.Available && !result.Passed {
			externalFailed = true
		}
	}
	if externalFailed {
		status = "failed"
	} else if externalUnavailable && status == "observed" {
		status = "unavailable"
	}
	report := map[string]any{"localFindings": findings, "external": external}
	reason := ""
	if externalUnavailable {
		reason = "one or more requested external scanners were unavailable"
	}
	if err := recordEvidence(cwd, "security", "rsch security", status, fmt.Sprintf("scanned workspace and found %d local findings and %d external scanner results", len(findings), len(external)), reason, report); err != nil {
		return err
	}
	format, _ := cmd.Flags().GetString("format")
	if format == "json" {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	} else {
		if len(findings) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no secret findings")
		}
		for _, finding := range findings {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d %s\n", finding.Severity, finding.File, finding.Line, finding.Rule)
		}
		for _, result := range external {
			state := "unavailable"
			if result.Available {
				state = "passed"
				if !result.Passed {
					state = "failed"
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "external %s: %s", result.Name, state)
			if result.Reason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " (%s)", result.Reason)
			}
			fmt.Fprintln(cmd.OutOrStdout())
		}
	}
	if len(findings) > 0 {
		return fmt.Errorf("security scan found %d finding(s)", len(findings))
	}
	if externalFailed {
		return fmt.Errorf("external security scanner reported findings or failed")
	}
	if externalUnavailable {
		return fmt.Errorf("requested external security scanner unavailable")
	}
	return nil
}}

func init() {
	securityCmd.Flags().String("format", "text", "Output format (text or json)")
	securityCmd.Flags().StringSlice("external", nil, "Explicit external scanners: govulncheck, npm-audit, pip-audit, trivy")
	rootCmd.AddCommand(securityCmd)
}
