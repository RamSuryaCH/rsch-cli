package experiment

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestSafeExperimentIDs(t *testing.T) {
	for _, id := range []string{"", "../escape", "a/b", ".", ".."} {
		if safeID(id) {
			t.Fatalf("expected unsafe id: %q", id)
		}
	}
	if !safeID("release-ui") {
		t.Fatal("expected valid id")
	}
}

func TestRecommendDoesNotTreatUnavailableCostAsZero(t *testing.T) {
	comparison := Comparison{
		Clean:       true,
		Tests:       Evidence{Available: true, Passed: true},
		Performance: Evidence{Available: true, Passed: true},
		Security:    SecurityCheck{Available: true, Passed: true},
		Cost:        Evidence{Available: false, Reason: "no session telemetry"},
	}
	recommendation := Recommend(comparison)
	if recommendation.Action != "conditional-review" || recommendation.EvidenceComplete {
		t.Fatalf("recommendation=%+v", recommendation)
	}
	comparison.Cost = Evidence{Available: true, Passed: true}
	recommendation = Recommend(comparison)
	if recommendation.Action != "consider-merge" || !recommendation.EvidenceComplete {
		t.Fatalf("complete recommendation=%+v", recommendation)
	}
}

func TestUnavailableComparisonPreservesExplicitAssessment(t *testing.T) {
	comparison := unavailableComparison(Record{ID: "exp-1"}, "git unavailable")
	if comparison.Tests.Available || comparison.Performance.Available || comparison.Security.Available || comparison.Complexity.Available {
		t.Fatalf("unavailable dimensions were reported as available: %+v", comparison)
	}
	if comparison.Recommendation.Action != "conditional-review" || comparison.Recommendation.EvidenceComplete {
		t.Fatalf("unexpected unavailable recommendation: %+v", comparison.Recommendation)
	}
}

func TestRunPerformanceRejectsCanceledBuildWithoutOutput(t *testing.T) {
	root := t.TempDir()
	writeExperimentFile(t, filepath.Join(root, "go.mod"), "module example.com/canceled\n\ngo 1.24\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	evidence := runPerformance(ctx, root)
	if evidence.Passed || evidence.Output == "" {
		t.Fatalf("canceled build was not preserved as failed evidence: %+v", evidence)
	}
}

func TestRunSecurityRejectsIncompleteCoverage(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"oversized file", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "large.txt"), make([]byte, (2<<20)+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"file limit", func(t *testing.T, root string) {
			for i := 0; i <= 1000; i++ {
				if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d.txt", i)), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"unreadable file", func(t *testing.T, root string) {
			path := filepath.Join(root, "unreadable.txt")
			if err := os.WriteFile(path, []byte("unreadable"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
			if _, err := os.ReadFile(path); err == nil {
				t.Skip("current user can read files without read permission")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			check := runSecurity(context.Background(), root)
			if check.Available || check.Passed || check.Reason == "" {
				t.Fatalf("incomplete security scan was reported as successful evidence: %+v", check)
			}
			if check.Coverage == nil || check.Coverage.Complete {
				t.Fatalf("missing incomplete coverage metadata: %+v", check)
			}
			comparison := Comparison{Clean: true, CleanAvailable: true, Tests: Evidence{Available: true, Passed: true}, Performance: Evidence{Available: true, Passed: true}, Cost: Evidence{Available: true, Passed: true}, Security: check}
			recommendation := Recommend(comparison)
			if recommendation.EvidenceComplete || recommendation.Action == "consider-merge" {
				t.Fatalf("incomplete security scan enabled merge recommendation: %+v", recommendation)
			}
		})
	}
}

func TestRecommendRejectsFindingsEvenWhenSecurityCoverageIsIncomplete(t *testing.T) {
	comparison := Comparison{
		Clean: true, CleanAvailable: true,
		Tests: Evidence{Available: true, Passed: true}, Performance: Evidence{Available: true, Passed: true}, Cost: Evidence{Available: true, Passed: true},
		Security: SecurityCheck{Available: false, HighOrCritical: 1, Reason: "security scan incomplete"},
	}
	recommendation := Recommend(comparison)
	if recommendation.Action != "do-not-merge" || recommendation.EvidenceComplete {
		t.Fatalf("observed security finding was hidden by incomplete coverage: %+v", recommendation)
	}
}

func TestExperimentLifecycleUsesRealTemporaryGitWorktree(t *testing.T) {
	if !process.Exists("git") {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	experimentGitTestCommand(t, root, "init", "-q")
	experimentGitTestCommand(t, root, "config", "user.email", "rsch-test@example.invalid")
	experimentGitTestCommand(t, root, "config", "user.name", "RSCH Test")
	writeExperimentFile(t, filepath.Join(root, "go.mod"), "module example.com/experiment\n\ngo 1.24\n")
	writeExperimentFile(t, filepath.Join(root, "main.go"), "package experiment\n\nfunc Value() string { return \"base\" }\n")
	experimentGitTestCommand(t, root, "add", "go.mod", "main.go")
	experimentGitTestCommand(t, root, "commit", "-qm", "base")

	manager := NewManager(root)
	record, err := manager.Start(context.Background(), root, "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if record.Base == "" || record.Status != "active" {
		t.Fatalf("unexpected record: %+v", record)
	}
	writeExperimentFile(t, filepath.Join(record.Worktree, "main.go"), "package experiment\n\nfunc Value() string { return \"candidate\" }\n")
	comparison, err := manager.Compare(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Clean || len(comparison.ChangedFiles) != 1 || !comparison.Tests.Available || !comparison.Tests.Passed || !comparison.Performance.Available || !comparison.Performance.Passed || !comparison.Security.Available || !comparison.Security.Passed {
		t.Fatalf("unexpected experiment comparison: %+v", comparison)
	}
	if comparison.Recommendation.Action != "do-not-merge" || comparison.Recommendation.EvidenceComplete {
		t.Fatalf("dirty candidate was not conservatively rejected: %+v", comparison.Recommendation)
	}
	experimentGitTestCommand(t, record.Worktree, "checkout", "--", ".")
	if err := manager.Close(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	closed, err := manager.load(record.ID)
	if err != nil || closed.Status != "closed" {
		t.Fatalf("experiment was not closed: %+v %v", closed, err)
	}
}

func experimentGitTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	result, err := process.RunOutput(context.Background(), process.Spec{Argv: append([]string{"git"}, args...), Directory: root})
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, result.Output)
	}
}

func writeExperimentFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
