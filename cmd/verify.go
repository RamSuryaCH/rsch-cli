package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/browser"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/verification"
	"github.com/spf13/cobra"
)

var verifyCmd = &cobra.Command{Use: "verify", Short: "Run evidence-based project verification", RunE: func(cmd *cobra.Command, _ []string) error {
	gateName, err := cmd.Flags().GetString("gate")
	if err != nil {
		return err
	}
	if gateName != "" && gateName != "git" && gateName != "webdriver" {
		return newUsageError("unsupported verification gate %q; supported gates: git, webdriver", gateName)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if gateName == "git" || gateName == "webdriver" {
		var gate verification.Gate
		if gateName == "git" {
			gate = runGitGate(cmd.Context())
		} else {
			gate = runWebDriverGate(cmd.Context())
		}
		report := verification.ReportForGates([]verification.Gate{gate})
		if err := recordEvidence(cwd, "verification", "rsch verify --gate "+gateName, evidenceStatus(report), "runtime gate result", "", gate); err != nil {
			return err
		}
		return printVerification(cmd, report)
	}
	checks := make([]verification.Check, 0, 2)
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
		checks = append(checks, verification.Check{ID: "go-test", Name: "Go tests", Command: []string{"go", "test", "./..."}, Required: true}, verification.Check{ID: "go-vet", Name: "Go vet", Command: []string{"go", "vet", "./..."}, Required: true})
	} else if process.Exists("git") {
		checks = append(checks, verification.Check{ID: "git-readable", Name: "Git repository readable", Command: []string{"git", "status", "--porcelain"}, Required: true})
	}
	if len(checks) == 0 {
		return fmt.Errorf("no supported verification checks found in %s", cwd)
	}
	report := verification.Run(cmd.Context(), checks, cwd)
	// The unqualified verifier is the release decision. Include every
	// required runtime gate here so a green unit/static result cannot hide an
	// unavailable external capability such as WebDriver.
	runtimeReport := verification.ReportForGates([]verification.Gate{
		runGitGate(cmd.Context()),
		runWebDriverGate(cmd.Context()),
	})
	report.Gates = append(report.Gates, runtimeReport.Gates...)
	report.Passed += runtimeReport.Passed
	report.Failed += runtimeReport.Failed
	report.Blocked = append(report.Blocked, runtimeReport.Blocked...)
	report.Unverified = append(report.Unverified, runtimeReport.Unverified...)
	report.Complete = report.Complete && runtimeReport.Complete
	if !report.Complete {
		report.ReleaseStatus = "NOT_READY"
	}
	denominator := len(report.Gates)
	if denominator > 0 {
		report.Confidence = float64(report.Passed) / float64(denominator) * 100
	}
	return printVerification(cmd, report)
}}

func printVerification(cmd *cobra.Command, report verification.Report) error {
	ciMode, _ := cmd.Flags().GetBool("ci")
	jsonMode, _ := cmd.Flags().GetBool("json")
	if ciMode {
		// CI consumers receive stable structured output and the same required
		// check exit status, without interactive progress or spinner output.
		jsonMode = true
	}
	status := evidenceStatus(report)
	if !report.Complete {
		status = "failed"
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := recordEvidence(cwd, "verification", "rsch verify", status, fmt.Sprintf("ran %d verification checks and %d gates", len(report.Results), len(report.Gates)), "", report); err != nil {
		return err
	}
	if jsonMode {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, result := range report.Results {
			state := "passed"
			if !result.Passed {
				state = "failed"
			}
			fmt.Printf("%s: %s\n", result.Name, state)
		}
		for _, gate := range report.Gates {
			fmt.Printf("%s: %s\n", gate.Name, gate.Status)
		}
		fmt.Printf("confidence: %.0f%%\nrelease status: %s\n", report.Confidence, report.ReleaseStatus)
	}
	if !report.Complete {
		return fmt.Errorf("verification failed")
	}
	return nil
}

func evidenceStatus(report verification.Report) string {
	if report.Complete {
		return "observed"
	}
	return "failed"
}

func runGitGate(ctx context.Context) verification.Gate {
	gate := verification.Gate{ID: "git", Name: "Git runtime experiment", Required: true, Environment: runtime.GOOS + "/" + runtime.GOARCH, Timestamp: timeNow(), Command: []string{"git", "init", "add", "commit", "status", "diff"}}
	if !process.Exists("git") {
		gate.Status = verification.Blocked
		gate.Evidence = "git executable is unavailable"
		return gate
	}
	fixture, err := os.MkdirTemp("", "rsch-git-gate-")
	if err != nil {
		gate.Status, gate.Evidence = verification.Fail, err.Error()
		return gate
	}
	defer os.RemoveAll(fixture)
	commands := [][]string{
		{"git", "-C", fixture, "init", "-q"},
	}
	for _, argv := range commands {
		if result, runErr := process.RunOutput(ctx, process.Spec{Argv: argv}); runErr != nil {
			gate.Status, gate.Evidence = verification.Fail, strings.TrimSpace(string(result.Output)+" "+runErr.Error())
			return gate
		}
	}
	path := filepath.Join(fixture, "fixture.txt")
	if err := os.WriteFile(path, []byte("initial\n"), 0o600); err != nil {
		gate.Status, gate.Evidence = verification.Fail, err.Error()
		return gate
	}
	for _, argv := range [][]string{
		{"git", "-C", fixture, "add", "fixture.txt"},
		{"git", "-C", fixture, "-c", "user.name=RSCH Gate", "-c", "user.email=gate@example.invalid", "commit", "-qm", "initial"},
	} {
		if result, runErr := process.RunOutput(ctx, process.Spec{Argv: argv}); runErr != nil {
			gate.Status, gate.Evidence = verification.Fail, strings.TrimSpace(string(result.Output)+" "+runErr.Error())
			return gate
		}
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		gate.Status, gate.Evidence = verification.Fail, err.Error()
		return gate
	}
	status, statusErr := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", fixture, "status", "--porcelain"}})
	diff, diffErr := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", fixture, "diff", "--", "fixture.txt"}})
	if statusErr != nil || diffErr != nil || strings.TrimSpace(string(status.Output)) == "" || strings.TrimSpace(string(diff.Output)) == "" {
		gate.Status = verification.Fail
		gate.Evidence = "fixture status/diff validation failed"
		return gate
	}
	gate.Status = verification.Pass
	gate.Evidence = "isolated fixture initialized, committed, modified, and verified with status and diff"
	gate.Artifact = "temporary fixture cleaned after verification"
	return gate
}

func runWebDriverGate(ctx context.Context) verification.Gate {
	gate := verification.Gate{ID: "webdriver", Name: "WebDriver runtime", Required: true, Environment: runtime.GOOS + "/" + runtime.GOARCH, Timestamp: timeNow(), Command: []string{"RSCH_WEBDRIVER_URL", "POST /session", "execute /session/{id}/execute/sync", "DELETE /session/{id}"}}
	endpoint := strings.TrimSpace(os.Getenv("RSCH_WEBDRIVER_URL"))
	if endpoint == "" {
		gate.Status = verification.Blocked
		gate.Evidence = "WebDriver endpoint is not configured; set RSCH_WEBDRIVER_URL"
		return gate
	}
	client, err := browser.New(endpoint, browser.Policy{AllowedHosts: []string{"localhost", "127.0.0.1"}, AllowPrivate: true})
	if err != nil {
		gate.Status, gate.Evidence = verification.Fail, err.Error()
		return gate
	}
	if err := client.Start(ctx, os.Getenv("RSCH_BROWSER")); err != nil {
		gate.Status, gate.Evidence = verification.Fail, err.Error()
		return gate
	}
	defer client.Close(ctx)
	value, err := client.Evaluate(ctx, "return 1 + 1", nil)
	if err != nil || fmt.Sprint(value) != "2" {
		gate.Status = verification.Fail
		gate.Evidence = "native session started but interaction validation failed: " + fmt.Sprint(err)
		return gate
	}
	gate.Status = verification.Pass
	gate.Evidence = "native WebDriver session created, script interaction validated, and session terminated"
	return gate
}

func timeNow() time.Time { return time.Now().UTC() }

func init() {
	verifyCmd.Flags().Bool("json", false, "Output structured JSON")
	verifyCmd.Flags().Bool("ci", false, "CI mode (same checks, stable exit status)")
	verifyCmd.Flags().String("gate", "", "Run one authoritative runtime gate: git or webdriver")
	rootCmd.AddCommand(verifyCmd)
}
