// Package experiment manages isolated, recoverable Git worktree experiments.
package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/gitsafety"
	"github.com/opencode-ai/opencode/internal/performance"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
	"github.com/opencode-ai/opencode/internal/worktree"
)

type Record struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Worktree          string    `json:"worktree"`
	Base              string    `json:"base"`
	CreatedAt         time.Time `json:"createdAt"`
	Status            string    `json:"status"`
	ParentStateCopied bool      `json:"parentStateCopied,omitempty"`
}
type Comparison struct {
	Record         Record         `json:"record"`
	Clean          bool           `json:"clean"`
	CleanAvailable bool           `json:"cleanAvailable"`
	Entries        []string       `json:"entries,omitempty"`
	DiffStat       string         `json:"diffStat,omitempty"`
	ChangedFiles   []string       `json:"changedFiles,omitempty"`
	Tests          Evidence       `json:"tests"`
	Performance    Evidence       `json:"performance"`
	Security       SecurityCheck  `json:"security"`
	Complexity     Complexity     `json:"complexity"`
	Cost           Evidence       `json:"cost"`
	Recommendation Recommendation `json:"recommendation"`
}

type Recommendation struct {
	Action           string   `json:"action"`
	EvidenceComplete bool     `json:"evidenceComplete"`
	Reasons          []string `json:"reasons"`
}
type Evidence struct {
	Available bool          `json:"available"`
	Passed    bool          `json:"passed"`
	Duration  time.Duration `json:"duration,omitempty"`
	Output    string        `json:"output,omitempty"`
	Reason    string        `json:"reason,omitempty"`
}
type SecurityCheck struct {
	Available      bool                   `json:"available"`
	Passed         bool                   `json:"passed"`
	Findings       int                    `json:"findings"`
	HighOrCritical int                    `json:"highOrCritical"`
	Reason         string                 `json:"reason,omitempty"`
	Coverage       *security.ScanCoverage `json:"coverage,omitempty"`
}
type Complexity struct {
	Available    bool   `json:"available"`
	ChangedFiles int    `json:"changedFiles"`
	DiffLines    string `json:"diffLines,omitempty"`
	Reason       string `json:"reason,omitempty"`
}
type Manager struct {
	Root         string
	MaxWorktrees int
}

func NewManager(workspace string, limits ...int) Manager {
	max := 0
	if len(limits) > 0 && limits[0] > 0 {
		max = limits[0]
	}
	return Manager{Root: filepath.Join(workspace, ".rsch", "experiments"), MaxWorktrees: max}
}
func (m Manager) path(id string) string { return filepath.Join(m.Root, id+".json") }
func (m Manager) Start(ctx context.Context, workspace, id string) (Record, error) {
	return m.start(ctx, workspace, id, false)
}

// StartFork creates an isolated alternative workflow while preserving dirty
// parent changes in the child worktree. The parent remains untouched.
func (m Manager) StartFork(ctx context.Context, workspace, id string) (Record, error) {
	return m.start(ctx, workspace, id, true)
}

func (m Manager) start(ctx context.Context, workspace, id string, preserveParent bool) (Record, error) {
	if !safeID(id) {
		return Record{}, errors.New("invalid experiment id")
	}
	if _, err := os.Stat(m.path(id)); err == nil {
		return Record{}, errors.New("experiment already exists")
	}
	manager := worktree.NewManagerWithLimit(workspace, m.MaxWorktrees)
	var path string
	var err error
	if preserveParent {
		path, err = manager.CreateFork(ctx, workspace, id)
	} else {
		path, err = manager.Create(ctx, workspace, id)
	}
	if err != nil {
		return Record{}, err
	}
	base, err := gitOutput(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		return Record{}, err
	}
	record := Record{ID: id, Name: id, Worktree: path, Base: strings.TrimSpace(base), CreatedAt: time.Now().UTC(), Status: "active", ParentStateCopied: preserveParent}
	if err := os.MkdirAll(m.Root, 0o700); err != nil {
		return Record{}, err
	}
	if err := m.save(record); err != nil {
		return Record{}, err
	}
	return record, nil
}
func (m Manager) List() ([]Record, error) {
	entries, err := os.ReadDir(m.Root)
	if os.IsNotExist(err) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := []Record{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.Root, entry.Name()))
		if err != nil {
			return nil, err
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}
func (m Manager) Compare(ctx context.Context, id string) (Comparison, error) {
	record, err := m.load(id)
	if err != nil {
		return Comparison{}, err
	}
	status, err := gitsafety.Inspect(ctx, record.Worktree)
	if err != nil {
		return unavailableComparison(record, "Git/worktree inspection unavailable: "+err.Error()), nil
	}
	stat, err := gitOutput(ctx, record.Worktree, "diff", "--stat", record.Base)
	if err != nil {
		return unavailableComparison(record, "experiment diff statistics unavailable: "+err.Error()), nil
	}
	filesOutput, err := gitOutput(ctx, record.Worktree, "diff", "--name-only", record.Base)
	if err != nil {
		return unavailableComparison(record, "experiment changed-file inspection unavailable: "+err.Error()), nil
	}
	files := nonEmptyLines(filesOutput)
	comparison := Comparison{Record: record, Clean: status.Clean, CleanAvailable: true, Entries: status.Entries, DiffStat: strings.TrimSpace(stat), ChangedFiles: files, Complexity: Complexity{Available: true, ChangedFiles: len(files), DiffLines: strings.TrimSpace(stat)}, Cost: Evidence{Available: false, Reason: "experiment cost telemetry is not persisted in the Git worktree"}}
	comparison.Tests = runTests(ctx, record.Worktree)
	comparison.Performance = runPerformance(ctx, record.Worktree)
	comparison.Security = runSecurity(ctx, record.Worktree)
	comparison.Recommendation = Recommend(comparison)
	return comparison, nil
}

func unavailableComparison(record Record, reason string) Comparison {
	comparison := Comparison{
		Record:      record,
		Tests:       Evidence{Available: false, Reason: reason},
		Performance: Evidence{Available: false, Reason: reason},
		Security:    SecurityCheck{Available: false, Reason: reason},
		Complexity:  Complexity{Available: false, Reason: reason},
		Cost:        Evidence{Available: false, Reason: "experiment cost telemetry is not persisted in the Git worktree"},
	}
	comparison.Recommendation = Recommend(comparison)
	return comparison
}

// Recommend only makes a merge recommendation when all required comparison
// dimensions were observed. Cost is intentionally optional: when unavailable
// the result is conditional rather than silently treating cost as zero.
func Recommend(comparison Comparison) Recommendation {
	reasons := make([]string, 0)
	if !comparison.Tests.Available {
		reasons = append(reasons, "test evidence unavailable: "+comparison.Tests.Reason)
	} else if !comparison.Tests.Passed {
		reasons = append(reasons, "tests failed")
	}
	if !comparison.Performance.Available {
		reasons = append(reasons, "performance evidence unavailable: "+comparison.Performance.Reason)
	} else if !comparison.Performance.Passed {
		reasons = append(reasons, "performance/build check failed")
	}
	if !comparison.Security.Available {
		reasons = append(reasons, "security evidence unavailable: "+comparison.Security.Reason)
	}
	if comparison.Security.HighOrCritical > 0 || comparison.Security.Available && !comparison.Security.Passed {
		reasons = append(reasons, "security check failed")
	}
	if !comparison.Clean {
		if comparison.CleanAvailable {
			reasons = append(reasons, "experiment worktree is not clean")
		} else {
			reasons = append(reasons, "experiment cleanliness evidence unavailable")
		}
	}
	if !comparison.Cost.Available {
		reasons = append(reasons, "cost evidence unavailable: "+comparison.Cost.Reason)
	}
	if len(reasons) == 0 {
		return Recommendation{Action: "consider-merge", EvidenceComplete: true, Reasons: []string{"required tests, performance, security, cleanliness, and cost evidence passed"}}
	}
	for _, reason := range reasons {
		if strings.Contains(reason, "failed") || strings.Contains(reason, "not clean") {
			return Recommendation{Action: "do-not-merge", EvidenceComplete: false, Reasons: reasons}
		}
	}
	return Recommendation{Action: "conditional-review", EvidenceComplete: false, Reasons: reasons}
}

func runTests(ctx context.Context, root string) Evidence {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return Evidence{Available: false, Reason: "no supported test manifest found"}
	}
	started := time.Now()
	check, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := process.RunOutput(check, process.Spec{Argv: []string{"go", "test", "./..."}, Directory: root})
	return Evidence{Available: true, Passed: err == nil, Duration: time.Since(started), Output: truncateEvidence(result.Output)}
}

func runPerformance(ctx context.Context, root string) Evidence {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return Evidence{Available: false, Reason: "no supported build manifest found"}
	}
	check, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	measurement := performance.MeasureBuild(check, root, 0)
	return Evidence{Available: true, Passed: measurement.Error == "", Duration: measurement.Duration, Output: truncateEvidence([]byte(measurement.Error))}
}

func runSecurity(ctx context.Context, root string) SecurityCheck {
	result := security.ScanWorkspace(ctx, root, security.ScanOptions{
		MaxFiles: 1000, MaxFileBytes: 2 << 20,
		ExcludeDirectories: []string{".git", ".rsch", "node_modules"},
	})
	return SecurityCheck{
		Available: result.Coverage.Complete, Passed: result.Coverage.Complete && result.HighOrCritical == 0,
		Findings: result.Findings, HighOrCritical: result.HighOrCritical,
		Coverage: &result.Coverage, Reason: result.Reason,
	}
}

func nonEmptyLines(value string) []string {
	lines := []string{}
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}
func truncateEvidence(value []byte) string {
	const max = 32 * 1024
	if len(value) > max {
		return string(value[:max]) + "\n[evidence truncated]"
	}
	return string(value)
}
func (m Manager) Close(ctx context.Context, id string) error {
	record, err := m.load(id)
	if err != nil {
		return err
	}
	status, err := gitsafety.Inspect(ctx, record.Worktree)
	if err != nil {
		return err
	}
	if !status.Clean {
		return errors.New("experiment worktree has changes; compare or clean it before closing")
	}
	if _, err := gitOutput(ctx, filepath.Dir(filepath.Dir(m.Root)), "worktree", "remove", record.Worktree); err != nil {
		return err
	}
	record.Status = "closed"
	return m.save(record)
}
func (m Manager) load(id string) (Record, error) {
	if !safeID(id) {
		return Record{}, errors.New("invalid experiment id")
	}
	data, err := os.ReadFile(m.path(id))
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}
func (m Manager) save(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	temp := m.path(record.ID) + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, m.path(record.ID)); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := process.RunOutput(ctx, process.Spec{Argv: append([]string{"git", "-C", dir}, args...)})
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(result.Output)))
	}
	return string(result.Output), nil
}
func safeID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\`)
}
