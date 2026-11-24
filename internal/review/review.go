// Package review performs deterministic, read-only change review checks.
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/feature"
	"github.com/opencode-ai/opencode/internal/gitsafety"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Assessment struct {
	Name     string   `json:"name"`
	Observed bool     `json:"observed"`
	Passed   bool     `json:"passed"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence,omitempty"`
}

type Report struct {
	Scope        string       `json:"scope"`
	GitAvailable bool         `json:"gitAvailable"`
	DiffChecked  bool         `json:"diffChecked"`
	CleanDiff    bool         `json:"cleanDiff"`
	Changed      []string     `json:"changed"`
	Findings     []Finding    `json:"findings"`
	Assessments  []Assessment `json:"assessments"`
	Ready        bool         `json:"ready"`
}

func Run(ctx context.Context, workspace string) (Report, error) {
	return run(ctx, workspace, false)
}

// RunSnapshot reviews the supported files in a workspace without claiming
// Git-backed patch integrity. Callers must opt into this scope explicitly.
func RunSnapshot(ctx context.Context, workspace string) (Report, error) {
	return run(ctx, workspace, true)
}

func run(ctx context.Context, workspace string, snapshot bool) (Report, error) {
	scope := "git-change"
	if snapshot {
		scope = "workspace-snapshot"
	}
	report := Report{Scope: scope, Findings: make([]Finding, 0), Assessments: make([]Assessment, 0), Changed: make([]string, 0)}
	var status gitsafety.Status
	var gitErr error
	if snapshot {
		report.Changed, gitErr = snapshotPaths(workspace)
		if gitErr != nil {
			return Report{}, gitErr
		}
		report.Assessments = append(report.Assessments, Assessment{
			Name: "correctness", Observed: true, Passed: true,
			Reason:   "workspace snapshot reviewed without claiming Git diff or patch integrity",
			Evidence: []string{"deterministic workspace file inventory"},
		})
	} else {
		status, gitErr = gitsafety.Inspect(ctx, workspace)
	}
	report.GitAvailable = !snapshot && gitErr == nil && status.Available
	if !snapshot && report.GitAvailable {
		report.Changed = status.Entries
		report.DiffChecked = true
		report.CleanDiff = true
		for _, args := range [][]string{{"diff", "--check"}, {"diff", "--cached", "--check"}} {
			argv := append([]string{"git", "-C", workspace}, args...)
			check, checkErr := process.RunOutput(ctx, process.Spec{Argv: argv})
			if checkErr != nil {
				report.CleanDiff = false
				message := strings.TrimSpace(string(check.Output))
				if message == "" {
					message = checkErr.Error()
				}
				report.Findings = append(report.Findings, Finding{Rule: "diff-check", Severity: "medium", Message: strings.Join(args, " ") + ": " + message})
			}
		}
	} else if !snapshot {
		reason := status.Reason
		if reason == "" && gitErr != nil {
			reason = gitErr.Error()
		}
		if reason == "" {
			reason = "Git inspection unavailable"
		}
		report.Assessments = append(report.Assessments, Assessment{Name: "git", Observed: false, Passed: false, Reason: "Git inspection unavailable: " + reason})
	}
	if !snapshot && report.GitAvailable {
		for _, entry := range status.Entries {
			lower := strings.ToLower(entry)
			if strings.Contains(lower, "auth") || strings.Contains(lower, "security") || strings.Contains(lower, "migration") {
				report.Findings = append(report.Findings, Finding{Rule: "high-risk-path", Severity: "high", Message: "independent security or migration review is required for " + entry})
			}
		}
	}
	if !snapshot && report.GitAvailable {
		report.Assessments = append(report.Assessments, Assessment{Name: "correctness", Observed: true, Passed: report.CleanDiff, Reason: "Git staged and unstaged diff whitespace and patch integrity checks completed", Evidence: []string{"git diff --check", "git diff --cached --check"}})
	}
	changedPaths := []string{}
	var pathsErr error
	if snapshot {
		changedPaths = append(changedPaths, report.Changed...)
	} else if report.GitAvailable {
		changedPaths, pathsErr = changedPathsForReview(ctx, workspace)
	}
	if _, err := os.Stat(filepath.Join(workspace, "go.mod")); err == nil {
		test := runCheck(ctx, workspace, "go test ./...")
		report.Assessments = append(report.Assessments, Assessment{Name: "testing", Observed: true, Passed: test.Passed, Reason: test.Reason, Evidence: []string{"go test ./..."}})
		vet := runCheck(ctx, workspace, "go vet ./...")
		report.Assessments = append(report.Assessments, Assessment{Name: "architecture", Observed: true, Passed: vet.Passed, Reason: vet.Reason, Evidence: []string{"go vet ./..."}})
		performanceReason := test.Reason
		report.Assessments = append(report.Assessments, Assessment{Name: "performance", Observed: true, Passed: test.Passed, Reason: "bounded repository test runtime observed; no benchmark claim made: " + performanceReason, Evidence: []string{"go test ./..."}})
	} else {
		report.Assessments = append(report.Assessments,
			Assessment{Name: "testing", Observed: false, Passed: false, Reason: "no supported test manifest found"},
			Assessment{Name: "architecture", Observed: false, Passed: false, Reason: "no supported static-analysis manifest found"},
			Assessment{Name: "performance", Observed: false, Passed: false, Reason: "no supported performance command configured"})
	}
	securityAssessment := inspectSecurity(workspace, changedPaths)
	if pathsErr != nil {
		securityAssessment = Assessment{Name: "security", Observed: false, Passed: false, Reason: "changed file inventory unavailable: " + pathsErr.Error()}
	}
	report.Assessments = append(report.Assessments, securityAssessment)
	registry, registryErr := feature.Load(workspace)
	if registryErr != nil {
		report.Assessments = append(report.Assessments, Assessment{Name: "specification", Observed: false, Passed: false, Reason: "feature registry unavailable: " + registryErr.Error()})
	} else {
		report.Assessments = append(report.Assessments, Assessment{Name: "specification", Observed: true, Passed: len(registry.Features) > 0, Reason: fmt.Sprintf("validated %d registered features", len(registry.Features)), Evidence: []string{".rsch/product/features/registry.json"}})
	}
	maintainabilityPassed := len(changedPaths) <= 1000
	report.Assessments = append(report.Assessments, Assessment{Name: "maintainability", Observed: true, Passed: maintainabilityPassed, Reason: fmt.Sprintf("reviewed %d %s paths", len(changedPaths), map[bool]string{true: "snapshot", false: "changed"}[snapshot])})
	regressionPassed := maintainabilityPassed
	regressionReason := "no bounded-change failure observed; Git patch integrity is outside snapshot scope"
	if !snapshot {
		regressionPassed = report.CleanDiff && maintainabilityPassed
		regressionReason = "no patch-integrity or bounded-change failure observed"
	}
	report.Assessments = append(report.Assessments, Assessment{Name: "regression-risk", Observed: true, Passed: regressionPassed, Reason: regressionReason})
	report.Ready = snapshot || report.CleanDiff
	for _, finding := range report.Findings {
		if finding.Severity == "high" || finding.Severity == "critical" {
			report.Ready = false
		}
	}
	for _, assessment := range report.Assessments {
		if !assessment.Observed || !assessment.Passed {
			report.Ready = false
		}
	}
	return report, nil
}

var snapshotSkippedDirectories = map[string]bool{
	".git": true, ".rsch": true, ".idea": true, ".vscode": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"out": true, "coverage": true, "target": true, "tmp": true,
	"temp": true, ".cache": true,
}

var snapshotExtensions = map[string]bool{
	".bash": true, ".bat": true, ".c": true, ".cfg": true, ".conf": true,
	".cpp": true, ".cs": true, ".css": true, ".env": true, ".go": true,
	".h": true, ".hpp": true, ".htm": true, ".html": true, ".ini": true,
	".java": true, ".js": true, ".json": true, ".jsx": true, ".kt": true,
	".lock": true, ".md": true, ".mod": true, ".proto": true, ".py": true,
	".rb": true, ".rs": true, ".sh": true, ".sql": true, ".sum": true,
	".swift": true, ".toml": true, ".ts": true, ".tsx": true, ".txt": true,
	".xml": true, ".yaml": true, ".yml": true,
}

var snapshotNames = map[string]bool{"Dockerfile": true, "Makefile": true, "README": true, "LICENSE": true}

func snapshotPaths(workspace string) ([]string, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && snapshotSkippedDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() || info.Size() > 2<<20 {
			return nil
		}
		name := entry.Name()
		if !snapshotExtensions[strings.ToLower(filepath.Ext(name))] && !snapshotNames[name] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.IndexByte(string(data), 0) >= 0 {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

type commandCheck struct {
	Passed bool
	Reason string
}

func runCheck(ctx context.Context, workspace, command string) commandCheck {
	parts := strings.Fields(command)
	started := time.Now()
	result, err := process.RunOutput(ctx, process.Spec{Argv: parts, Directory: workspace})
	if err != nil {
		reason := strings.TrimSpace(string(result.Output))
		if reason == "" {
			reason = err.Error()
		}
		if len(reason) > 500 {
			reason = reason[len(reason)-500:]
		}
		return commandCheck{Reason: reason}
	}
	return commandCheck{Passed: true, Reason: fmt.Sprintf("passed in %s", time.Since(started).Round(time.Millisecond))}
}

func changedPathsForReview(ctx context.Context, workspace string) ([]string, error) {
	// Porcelain -z preserves whitespace, quotes and newlines in paths, and
	// works before the first commit. Rename records name the destination
	// first, followed by a separate NUL-terminated source path.
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "status", "--porcelain=v1", "-z", "--untracked-files=all"}})
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	records := strings.Split(string(result.Output), "\x00")
	for index := 0; index < len(records); index++ {
		entry := records[index]
		if entry == "" && index == len(records)-1 {
			continue
		}
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("malformed Git status record")
		}
		paths = append(paths, entry[3:])
		if strings.ContainsAny(entry[:2], "RC") {
			index++
			if index >= len(records) || records[index] == "" {
				return nil, fmt.Errorf("missing Git rename source")
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func inspectSecurity(workspace string, paths []string) Assessment {
	if len(paths) == 0 {
		return Assessment{Name: "security", Observed: true, Passed: true, Reason: "no changed files required scanning"}
	}
	findings := 0
	for _, path := range paths {
		absolute := path
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(workspace, filepath.FromSlash(path))
		}
		info, err := os.Stat(absolute)
		if err != nil || info.IsDir() || info.Size() > 2<<20 {
			continue
		}
		items, scanErr := security.ScanFile(absolute)
		if scanErr != nil {
			return Assessment{Name: "security", Observed: false, Passed: false, Reason: "security scan unavailable: " + scanErr.Error()}
		}
		for _, item := range items {
			if item.Severity == security.High || item.Severity == security.Critical {
				findings++
			}
		}
	}
	return Assessment{Name: "security", Observed: true, Passed: findings == 0, Reason: fmt.Sprintf("scanned review files; high or critical findings=%d", findings)}
}
