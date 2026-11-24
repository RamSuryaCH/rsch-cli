package verification

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestRunCalculatesEvidenceConfidence(t *testing.T) {
	command := "sh"
	args := []string{"-c", "exit 0"}
	if runtime.GOOS == "windows" {
		command = "cmd"
		args = []string{"/C", "exit /B 0"}
	}
	report := Run(context.Background(), []Check{{ID: "pass", Name: "passing check", Command: append([]string{command}, args...), Required: true}}, "")
	if !report.Complete || report.Confidence != 100 || report.Passed != 1 || report.Results[0].ExitCode != 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestRunUsesHostEnvironmentWhenCrossBuildOverridesAreUnset(t *testing.T) {
	t.Setenv("GOOS", "")
	t.Setenv("GOARCH", "")
	report := Run(context.Background(), []Check{{ID: "environment", Name: "environment", Command: []string{"go", "version"}, Required: true}}, "")
	if len(report.Gates) != 1 || report.Gates[0].Environment != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("unexpected environment evidence: %+v", report.Gates)
	}
}

func TestReportForGatesNeverTreatsBlockedAsReady(t *testing.T) {
	report := ReportForGates([]Gate{{ID: "webdriver", Name: "WebDriver", Status: Blocked, Required: true, Evidence: "not configured", Timestamp: time.Now().UTC()}})
	if report.Complete || report.ReleaseStatus != "NOT_READY" || len(report.Blocked) != 1 {
		t.Fatalf("blocked gate was treated as ready: %+v", report)
	}
}

func TestReportForGatesCalculatesConfidenceFromAllGates(t *testing.T) {
	for _, test := range []struct {
		name       string
		gates      []Gate
		confidence float64
		complete   bool
	}{
		{name: "empty"},
		{name: "all pass", gates: []Gate{{ID: "test", Status: Pass, Required: true}}, confidence: 100, complete: true},
		{name: "blocked gate", gates: []Gate{{ID: "test", Status: Pass, Required: true}, {ID: "runtime", Status: Blocked, Required: true}}, confidence: 50},
		{name: "optional failure", gates: []Gate{{ID: "test", Status: Pass, Required: true}, {ID: "optional", Status: Fail}}, confidence: 50, complete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := ReportForGates(test.gates)
			if report.Confidence != test.confidence || report.Complete != test.complete {
				t.Fatalf("report=%+v", report)
			}
		})
	}
}

func TestRunDoesNotReportSuccessfulExitForChecksThatNeverStarted(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name  string
		ctx   context.Context
		check Check
	}{
		{name: "invalid", ctx: context.Background(), check: Check{Required: true}},
		{name: "missing executable", ctx: context.Background(), check: Check{ID: "missing", Name: "Missing executable", Command: []string{filepath.Join(t.TempDir(), "missing")}, Required: true}},
		{name: "canceled", ctx: ctx, check: Check{ID: "canceled", Name: "Canceled", Command: []string{executable, "-test.run=^TestVerificationProcessExit$", "--", "--verification-exit", "0"}, Required: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := Run(test.ctx, []Check{test.check}, "")
			if report.Complete || report.ReleaseStatus != "NOT_READY" || len(report.Results) != 1 || report.Results[0].ExitCode != -1 || report.Results[0].Passed || report.Results[0].Error == "" {
				t.Fatalf("unexecuted check reported successful exit: %+v", report)
			}
		})
	}
}

func TestRunPreservesFailedProcessExitCode(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), []Check{{ID: "exit", Name: "Exit status", Command: []string{executable, "-test.run=^TestVerificationProcessExit$", "--", "--verification-exit", "7"}, Required: true}}, "")
	if report.Complete || len(report.Results) != 1 || report.Results[0].ExitCode != 7 || report.Results[0].Passed {
		t.Fatalf("failed exit status lost: %+v", report)
	}
}

func TestVerificationProcessExit(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--verification-exit" {
		return
	}
	code, err := strconv.Atoi(os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}
