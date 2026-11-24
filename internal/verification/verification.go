// Package verification runs explicit, reproducible checks and turns their
// results into structured evidence and a calculated confidence summary.
package verification

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
)

type GateStatus string

const (
	Pass          GateStatus = "PASS"
	Fail          GateStatus = "FAIL"
	Blocked       GateStatus = "BLOCKED"
	Unverified    GateStatus = "UNVERIFIED"
	NotApplicable GateStatus = "NOT_APPLICABLE"
)

type Check struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Command  []string      `json:"command"`
	Required bool          `json:"required"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}
type Result struct {
	CheckID  string        `json:"checkId"`
	Name     string        `json:"name"`
	Passed   bool          `json:"passed"`
	ExitCode int           `json:"exitCode"`
	Output   string        `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

type Gate struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Status      GateStatus `json:"status"`
	Required    bool       `json:"required"`
	Evidence    string     `json:"evidence,omitempty"`
	Environment string     `json:"environment,omitempty"`
	Command     []string   `json:"command,omitempty"`
	Timestamp   time.Time  `json:"timestamp"`
	Artifact    string     `json:"artifact,omitempty"`
}
type Report struct {
	Results       []Result `json:"results"`
	Gates         []Gate   `json:"gates"`
	Passed        int      `json:"passed"`
	Failed        int      `json:"failed"`
	Confidence    float64  `json:"confidence"`
	Complete      bool     `json:"complete"`
	ReleaseStatus string   `json:"releaseStatus"`
	Blocked       []string `json:"blocked,omitempty"`
	Unverified    []string `json:"unverified,omitempty"`
}

func Run(ctx context.Context, checks []Check, directory string) Report {
	report := Report{Results: make([]Result, 0, len(checks)), Gates: make([]Gate, 0, len(checks)), ReleaseStatus: "PASS"}
	for _, check := range checks {
		result := run(ctx, check, directory)
		report.Results = append(report.Results, result)
		status := Pass
		if !result.Passed {
			status = Fail
		}
		evidence := result.Output
		if result.Error != "" {
			evidence = result.Error
		}
		report.Gates = append(report.Gates, Gate{ID: check.ID, Name: check.Name, Status: status, Required: check.Required, Evidence: evidence, Environment: environment(), Command: append([]string(nil), check.Command...), Timestamp: time.Now().UTC()})
		if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	if len(report.Results) > 0 {
		report.Confidence = float64(report.Passed) / float64(len(report.Results)) * 100
	}
	report.Complete = true
	for index, check := range checks {
		if check.Required && !report.Results[index].Passed {
			report.Complete = false
			report.ReleaseStatus = "NOT_READY"
		}
	}
	if len(checks) == 0 {
		report.Complete = false
		report.ReleaseStatus = "NOT_READY"
	}
	return report
}

func ReportForGates(gates []Gate) Report {
	report := Report{Gates: append([]Gate(nil), gates...), ReleaseStatus: "PASS", Complete: true}
	for _, gate := range gates {
		switch gate.Status {
		case Pass:
			report.Passed++
		case Fail:
			report.Failed++
		case Blocked:
			report.Blocked = append(report.Blocked, gate.ID)
		case Unverified:
			report.Unverified = append(report.Unverified, gate.ID)
		}
		if gate.Required && gate.Status != Pass {
			report.Complete = false
			report.ReleaseStatus = "NOT_READY"
		}
	}
	if len(gates) == 0 {
		report.Complete = false
		report.ReleaseStatus = "NOT_READY"
	} else {
		report.Confidence = float64(report.Passed) / float64(len(gates)) * 100
	}
	return report
}

func environment() string {
	goos := os.Getenv("GOOS")
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := os.Getenv("GOARCH")
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos + "/" + goarch
}
func run(parent context.Context, check Check, directory string) Result {
	result := Result{CheckID: check.ID, Name: check.Name, ExitCode: -1}
	if check.ID == "" || check.Name == "" || len(check.Command) == 0 {
		result.Error = "check id, name, and command are required"
		return result
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx := parent
	var cancel context.CancelFunc
	if check.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, check.Timeout)
		defer cancel()
	}
	started := time.Now()
	processResult, err := process.RunOutput(ctx, process.Spec{Argv: check.Command, Directory: directory})
	result.Duration = time.Since(started)
	result.Output = string(processResult.Output)
	result.Passed = err == nil
	if !processResult.StartedAt.IsZero() {
		result.ExitCode = processResult.ExitCode
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}
