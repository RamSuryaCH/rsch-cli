package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestReportDefaultsToEmptyFindings(t *testing.T) {
	report := Report{Findings: make([]Finding, 0)}
	if report.Findings == nil {
		t.Fatal("findings must be structured")
	}
}

func TestRunPersistsUnavailableGitAssessmentInsteadOfReturningSetupError(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/review\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Assessments) == 0 || report.Assessments[0].Name != "git" || report.Assessments[0].Observed {
		t.Fatalf("Git unavailability was not reported explicitly: %+v", report.Assessments)
	}
}

func TestRunSnapshotIsExplicitAndCanPassWithoutGit(t *testing.T) {
	root := t.TempDir()
	writeReviewFile(t, filepath.Join(root, "go.mod"), "module example.com/review\n\ngo 1.24\n")
	writeReviewFile(t, filepath.Join(root, "review.go"), "package review\n\nfunc Review() {}\n")
	writeReviewFile(t, filepath.Join(root, "ignored.bin"), "binary\x00content")
	writeReviewFile(t, filepath.Join(root, "node_modules", "ignored.js"), "const ignored = true\n")
	writeReviewFile(t, filepath.Join(root, ".rsch", "evidence.json"), "{}\n")
	writeReviewFile(t, filepath.Join(root, ".rsch", "product", "features", "registry.json"), `{"version":1,"features":[{"id":"review","name":"Review","status":"planned"}]}`)

	report, err := RunSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scope != "workspace-snapshot" || report.GitAvailable || report.DiffChecked || report.CleanDiff {
		t.Fatalf("snapshot incorrectly claims Git evidence: %+v", report)
	}
	if !report.Ready {
		t.Fatalf("clean snapshot was not ready: %+v", report.Assessments)
	}
	if !contains(report.Changed, "review.go") || contains(report.Changed, "ignored.bin") || contains(report.Changed, "node_modules/ignored.js") || contains(report.Changed, ".rsch/evidence.json") {
		t.Fatalf("snapshot inventory included unsupported or generated paths: %v", report.Changed)
	}

	defaultReport, err := Run(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if defaultReport.Ready || defaultReport.Scope != "git-change" {
		t.Fatalf("default review must remain unavailable without Git: %+v", defaultReport)
	}
}

func TestSecurityAssessmentScansChangedFilesWithoutLeakingSecrets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.go")
	keyName := "token"
	source := "package config\nconst " + keyName + " = \"" + strings.Repeat("x", 32) + "\"\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	assessment := inspectSecurity(root, []string{"config.go"})
	if !assessment.Observed || assessment.Passed || assessment.Name != "security" {
		t.Fatalf("unexpected assessment: %+v", assessment)
	}
	if assessment.Reason == "" || filepath.Base(path) == assessment.Reason {
		t.Fatalf("security assessment lacks bounded reason: %+v", assessment)
	}
}

func TestRunChecksStagedWhitespace(t *testing.T) {
	root := t.TempDir()
	runReviewGit(t, root, "init", "-q")
	writeReviewFile(t, filepath.Join(root, "file.txt"), "staged trailing space \n")
	runReviewGit(t, root, "add", "file.txt")
	report, err := Run(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DiffChecked || report.CleanDiff {
		t.Fatalf("staged whitespace was not rejected: %+v", report)
	}
}

func TestChangedPathsIncludesUnbornAndUnusualNames(t *testing.T) {
	root := t.TempDir()
	runReviewGit(t, root, "init", "-q")
	paths := []string{"staged.go", " spaced.go", "line\nbreak.go", "quote\".go", "nested/untracked.go"}
	for _, path := range paths {
		writeReviewFile(t, filepath.Join(root, path), "package example\n")
	}
	runReviewGit(t, root, "add", "staged.go")
	got, err := changedPathsForReview(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !contains(got, path) {
			t.Errorf("changed paths %q omitted %q", got, path)
		}
	}
}

func TestChangedPathsUsesRenameDestination(t *testing.T) {
	root := t.TempDir()
	runReviewGit(t, root, "init", "-q")
	writeReviewFile(t, filepath.Join(root, "old.go"), "package example\n")
	runReviewGit(t, root, "add", "old.go")
	runReviewGit(t, root, "-c", "user.name=Review Test", "-c", "user.email=review@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "initial")
	name := "renamed\nfile.go"
	runReviewGit(t, root, "mv", "old.go", name)
	writeReviewFile(t, filepath.Join(root, "untracked.go"), "package example\n")
	got, err := changedPathsForReview(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !contains(got, name) || !contains(got, "untracked.go") {
		t.Fatalf("rename paths = %q; expected destination and subsequent untracked record", got)
	}
}

func TestChangedPathsReturnsInventoryFailure(t *testing.T) {
	if _, err := changedPathsForReview(context.Background(), t.TempDir()); err == nil {
		t.Fatal("unavailable Git inventory must not become an empty successful scan")
	}
}

func runReviewGit(t *testing.T, root string, args ...string) {
	t.Helper()
	if !process.Exists("git") {
		t.Skip("git unavailable")
	}
	argv := append([]string{"git", "-C", root}, args...)
	result, err := process.RunOutput(context.Background(), process.Spec{Argv: argv})
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, result.Output)
	}
}

func writeReviewFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
