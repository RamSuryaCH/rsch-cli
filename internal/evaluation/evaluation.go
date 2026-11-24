// Package evaluation runs reproducible local repository-quality checks. It does
// not claim to measure coding-agent task success or regression rate.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/feature"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

const RepositoryQualityEvaluation = "repository-quality"

type Check struct {
	Name     string                 `json:"name"`
	Passed   bool                   `json:"passed"`
	Duration time.Duration          `json:"duration"`
	Error    string                 `json:"error,omitempty"`
	Coverage *security.ScanCoverage `json:"coverage,omitempty"`
}
type Report struct {
	Version                 int       `json:"version"`
	RunID                   string    `json:"runId"`
	EvaluationType          string    `json:"evaluationType"`
	Artifact                string    `json:"artifact"`
	StartedAt               time.Time `json:"startedAt"`
	FinishedAt              time.Time `json:"finishedAt"`
	Checks                  []Check   `json:"checks"`
	RepositoryQualityPassed bool      `json:"repositoryQualityPassed"`
	TestSuccess             bool      `json:"testSuccess"`
	SpecCompliance          bool      `json:"specCompliance"`
	SecurityFailures        int       `json:"securityFailures"`
	AgentTaskEvaluated      bool      `json:"agentTaskEvaluated"`
	RegressionEvaluated     bool      `json:"regressionEvaluated"`
	RegressionReason        string    `json:"regressionReason"`
	// TaskSuccess and RegressionRate are retained only for Go source
	// compatibility. Repository-quality checks cannot establish either value,
	// so Run leaves them unset and omits them from serialized reports.
	TaskSuccess    bool          `json:"taskSuccess,omitempty"`
	RegressionRate float64       `json:"regressionRate,omitempty"`
	Limitations    []string      `json:"limitations"`
	Duration       time.Duration `json:"duration"`
	Telemetry      Telemetry     `json:"telemetry"`
}

// Telemetry states whether model-level measures were actually observed. The
// local regression runner does not invent token, cost, or human-intervention
// values when it executes only deterministic repository checks.
type Telemetry struct {
	TokensObserved            bool      `json:"tokensObserved"`
	CostObserved              bool      `json:"costObserved"`
	HumanInterventionObserved bool      `json:"humanInterventionObserved"`
	SessionsObserved          bool      `json:"sessionsObserved"`
	EventsObserved            bool      `json:"eventsObserved"`
	ModelUsageObserved        bool      `json:"modelUsageObserved"`
	Scope                     string    `json:"scope"`
	WindowStartedAt           time.Time `json:"windowStartedAt"`
	WindowFinishedAt          time.Time `json:"windowFinishedAt"`
	Sessions                  int64     `json:"sessions,omitempty"`
	ModelObservations         int64     `json:"modelObservations,omitempty"`
	PromptTokens              int64     `json:"promptTokens,omitempty"`
	CompletionTokens          int64     `json:"completionTokens,omitempty"`
	Cost                      float64   `json:"cost,omitempty"`
	EventCount                int64     `json:"eventCount,omitempty"`
	PermissionDecisions       int64     `json:"permissionDecisions,omitempty"`
	Reason                    string    `json:"reason"`
}

func newReport(started time.Time) Report {
	runID := uuid.NewString()
	return Report{
		Version:             2,
		RunID:               runID,
		EvaluationType:      RepositoryQualityEvaluation,
		Artifact:            filepath.ToSlash(filepath.Join(".rsch", "evaluation", "runs", runID+".json")),
		StartedAt:           started,
		Checks:              []Check{},
		AgentTaskEvaluated:  false,
		RegressionEvaluated: false,
		RegressionReason:    "no coding-task corpus or prior baseline was evaluated",
		Limitations: []string{
			"this report measures repository quality checks, not coding-agent task success",
			"regression rate is unavailable without a versioned task corpus and baseline",
			"workspace telemetry is time-windowed because persisted records do not yet carry an evaluation run id",
		},
	}
}

func Run(ctx context.Context, root string) (Report, error) {
	if strings.TrimSpace(root) == "" {
		return Report{}, errors.New("evaluation root is required")
	}
	started := time.Now().UTC()
	report := newReport(started)
	checks := []struct {
		name string
		args []string
	}{
		{"tests", []string{"go", "test", "-count=1", "./..."}},
		{"static-analysis", []string{"go", "vet", "./..."}},
	}
	if isGitWorkspace(ctx, root) {
		checks = append(checks, struct {
			name string
			args []string
		}{"diff-check", []string{"git", "diff", "--check"}})
	}
	for _, item := range checks {
		check := run(ctx, root, item.name, item.args)
		if item.name == "tests" {
			report.TestSuccess = check.Passed
		}
		report.Checks = append(report.Checks, check)
	}
	securityCheck := runSecurity(ctx, root)
	report.Checks = append(report.Checks, securityCheck)
	report.SecurityFailures = securityFailureCount(securityCheck)
	report.RepositoryQualityPassed = report.TestSuccess && allPassed(report.Checks)
	report.SpecCompliance = registryValid(root)
	if !report.SpecCompliance {
		report.RepositoryQualityPassed = false
	}
	report.FinishedAt = time.Now().UTC()
	report.Duration = report.FinishedAt.Sub(started)
	report.Telemetry = collectTelemetry(root, report.StartedAt, report.FinishedAt)
	return report, nil
}

func collectTelemetry(root string, startedAt, finishedAt time.Time) Telemetry {
	telemetry := Telemetry{
		Scope:            "workspace-time-window",
		WindowStartedAt:  startedAt,
		WindowFinishedAt: finishedAt,
		Reason:           "telemetry is limited to workspace records timestamped within this repository-quality evaluation window but cannot be attributed exclusively to this run; human intervention is unavailable because permission events do not identify manual versus automated denials",
	}
	databasePath := filepath.Join(root, ".rsch", "rsch.db")
	if _, err := os.Stat(databasePath); err != nil {
		return telemetry
	}
	connection, err := db.OpenReadOnlyAt(filepath.Join(root, ".rsch"))
	if err != nil {
		telemetry.Reason += "; persisted session telemetry unavailable: " + err.Error()
		return telemetry
	}
	defer connection.Close()
	startMillis, finishMillis := startedAt.UnixMilli(), finishedAt.UnixMilli()
	if err := connection.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT session_id), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cost), 0) FROM model_usage WHERE finished_at >= ? AND started_at <= ?`, startMillis, finishMillis).Scan(&telemetry.ModelObservations, &telemetry.Sessions, &telemetry.PromptTokens, &telemetry.CompletionTokens, &telemetry.Cost); err == nil {
		telemetry.ModelUsageObserved = true
		telemetry.SessionsObserved = telemetry.ModelObservations > 0
		telemetry.TokensObserved = telemetry.ModelObservations > 0
		telemetry.CostObserved = telemetry.ModelObservations > 0
	} else {
		telemetry.Reason += "; model-usage telemetry unavailable: " + err.Error()
	}
	if err := connection.QueryRow(`SELECT COUNT(*) FROM event_log WHERE occurred_at >= ? AND occurred_at <= ?`, startMillis, finishMillis).Scan(&telemetry.EventCount); err == nil {
		telemetry.EventsObserved = true
	} else {
		telemetry.Reason += "; lifecycle-event telemetry unavailable: " + err.Error()
	}
	if err := connection.QueryRow(`SELECT COUNT(*) FROM event_log WHERE occurred_at >= ? AND occurred_at <= ? AND type IN ('permission.granted', 'permission.denied', 'permission.auto_granted')`, startMillis, finishMillis).Scan(&telemetry.PermissionDecisions); err == nil {
		telemetry.Reason += "; permission decisions are counted separately and are not labeled human intervention"
	} else {
		telemetry.Reason += "; permission-decision telemetry unavailable: " + err.Error()
	}
	if telemetry.ModelUsageObserved {
		telemetry.Reason += "; model-usage records were queried for the run window"
	}
	if telemetry.EventsObserved {
		telemetry.Reason += "; lifecycle events were queried for the run window"
	}
	return telemetry
}

func allPassed(checks []Check) bool {
	for _, check := range checks {
		if !check.Passed {
			return false
		}
	}
	return true
}

func securityFailureCount(check Check) int {
	if check.Passed {
		return 0
	}
	// The scanner reports one bounded check failure. The detailed finding
	// count is included in the error so the result remains inspectable without
	// persisting secret-bearing source text.
	return 1
}

func runSecurity(ctx context.Context, root string) Check {
	started := time.Now()
	result := security.ScanWorkspace(ctx, root, security.ScanOptions{
		MaxFiles: 2000, MaxFileBytes: 2 << 20,
		ExcludeDirectories: []string{".git", ".rsch", "node_modules", "vendor", "dist", "build"},
	})
	check := Check{Name: "security", Passed: result.Coverage.Complete && result.HighOrCritical == 0, Duration: time.Since(started), Coverage: &result.Coverage, Error: result.Reason}
	if result.HighOrCritical > 0 {
		if check.Error != "" {
			check.Error += "; "
		}
		check.Error += fmt.Sprintf("security scan found %d high/critical finding(s)", result.HighOrCritical)
	}
	return check
}

func isGitWorkspace(ctx context.Context, root string) bool {
	_, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", root, "rev-parse", "--is-inside-work-tree"}})
	return err == nil
}

func Save(root string, report Report) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("evaluation root is required")
	}
	if !safeArtifactID(report.RunID) {
		return errors.New("evaluation run id is required and must be path-safe")
	}
	runsDirectory := filepath.Join(root, ".rsch", "evaluation", "runs")
	if err := os.MkdirAll(runsDirectory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	runPath := filepath.Join(runsDirectory, report.RunID+".json")
	file, err := os.OpenFile(runPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create immutable evaluation artifact: %w", err)
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(runPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write immutable evaluation artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close immutable evaluation artifact: %w", err)
	}
	written = true
	latestPath := filepath.Join(root, ".rsch", "evaluation", "latest.json")
	if err := writeLatest(latestPath, data); err != nil {
		return fmt.Errorf("update latest evaluation pointer: %w", err)
	}
	return nil
}

func safeArtifactID(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return filepath.Base(value) == value
}

func writeLatest(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".latest-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func run(ctx context.Context, root, name string, args []string) Check {
	started := time.Now()
	result, err := process.RunOutput(ctx, process.Spec{Argv: args, Directory: root})
	check := Check{Name: name, Passed: err == nil, Duration: time.Since(started)}
	if err != nil {
		check.Error = strings.TrimSpace(string(result.Output))
		if len(check.Error) > 500 {
			check.Error = check.Error[len(check.Error)-500:]
		}
	}
	return check
}

func registryValid(root string) bool {
	registry, err := feature.Load(root)
	return err == nil && len(registry.Features) > 0
}
