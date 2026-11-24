package dependencies

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestScanNormalizesPackageAndPythonDependencies(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"react":"^19"},"devDependencies":{"vitest":"^3"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte("requests==2.32.0\n# ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Dependencies) != 3 {
		t.Fatalf("expected 3 dependencies, got %+v", report.Dependencies)
	}
	if report.Dependencies[0].Name == "" || report.Dependencies[0].SourceFile == "" {
		t.Fatal("missing dependency provenance")
	}
}

func TestScanPreservesToolFindingsAlongsideDuplicates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tool fixtures require a POSIX shell")
	}
	bin := t.TempDir()
	fixtures := map[string]string{
		"go":          `{"Path":"example.com/dependency","Version":"v1.0.0","Update":{"Version":"v1.1.0"}}`,
		"govulncheck": `{"config":{"scanner_name":"govulncheck"}}`,
		"npm":         `{}`,
	}
	for name, output := range fixtures {
		script := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"react":"18"},"devDependencies":{"react":"19"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]int)
	for _, finding := range report.Findings {
		found[finding.Kind]++
	}
	for _, kind := range []string{"outdated", "security-audit", "duplicate-or-conflict"} {
		if found[kind] != 1 {
			t.Errorf("expected one %q finding, got %d: %+v", kind, found[kind], report.Findings)
		}
	}
}

func TestParseGoModFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\nrequire (\n\tgithub.com/acme/a v1.2.3\n\tgithub.com/acme/b v0.1.0 // indirect\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items := parseGoMod(root)
	if len(items) != 2 || !items[0].Direct || items[1].Direct {
		t.Fatalf("unexpected go dependencies: %+v", items)
	}
}

func TestScanDiscoversMonorepoPackages(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "services", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "web", "package.json"), []byte(`{"dependencies":{"react":"^19"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "services", "api", "go.mod"), []byte("module example/api\n\nrequire github.com/acme/a v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Packages) != 2 || len(report.Dependencies) != 2 {
		t.Fatalf("packages=%+v dependencies=%+v", report.Packages, report.Dependencies)
	}
	seen := map[string]bool{}
	for _, item := range report.Dependencies {
		seen[item.SourceFile] = true
	}
	if !seen["apps/web/package.json"] || !seen["services/api/go.mod"] {
		t.Fatalf("missing monorepo provenance: %+v", report.Dependencies)
	}
}

func TestMarkAffectedPackagesUsesChangedPathProvenance(t *testing.T) {
	packages := []Package{
		{Path: "apps/web", Manifest: "apps/web/package.json"},
		{Path: "services/api", Manifest: "services/api/go.mod"},
	}
	markAffected(packages, []string{"apps/web/src/main.ts", "services/api/go.mod", "README.md"})
	if !packages[0].Affected || packages[1].AffectedFiles == nil {
		t.Fatalf("packages were not marked affected: %+v", packages)
	}
	if packages[0].AffectedFiles[0] != "apps/web/src/main.ts" || !reflect.DeepEqual(packages[1].AffectedFiles, []string{"services/api/go.mod"}) {
		t.Fatalf("unexpected affected files: %+v", packages)
	}
}

func TestParseChangedFilesHandlesRenamesAndRSCHState(t *testing.T) {
	got := parseChangedFiles(" M apps/web/main.ts\x00R  services/api/new.go\x00old.go\x00?? .rsch/evidence.json\x00")
	want := []string{"apps/web/main.ts", "old.go", "services/api/new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed files=%v want=%v", got, want)
	}
}

func TestParseChangedFilesPreservesLiteralPaths(t *testing.T) {
	got := parseChangedFiles("?? apps/世界/main.ts\x00 M literal -> filename.go\x00?? line\nbreak.go\x00?? \"quoted\".go\x00??  spaced .go\x00")
	want := []string{"apps/世界/main.ts", "literal -> filename.go", "line\nbreak.go", `"quoted".go`, " spaced .go"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed files=%q want=%q", got, want)
	}
}

func TestChangedFilesIsRelativeToNestedScanRoot(t *testing.T) {
	if !process.Exists("git") {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	if _, err := process.RunOutput(context.Background(), process.Spec{Argv: []string{"git", "init", "-q", root}}); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "outside.go"), filepath.Join(nested, "inside.go")} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, available, err := changedFiles(context.Background(), nested)
	if err != nil || !available || !reflect.DeepEqual(got, []string{"inside.go"}) {
		t.Fatalf("changed files=%q available=%v error=%v", got, available, err)
	}
}

func TestNPMOutdatedReportsFindingsWhenExitStatusIsOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tool fixture requires a POSIX shell")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '{\"react\":{\"current\":\"18\",\"latest\":\"19\"}}'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"react":"18"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Name != "react" || report.Findings[0].Available != "19" {
		t.Fatalf("npm outdated findings lost: %+v", report.Findings)
	}
}

func TestScanIgnoresSymlinkedManifests(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	outside := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(outside, []byte(`{"dependencies":{"outside":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "package.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	report, err := Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Packages) != 0 || len(report.Dependencies) != 0 {
		t.Fatalf("scanned manifest outside workspace: %+v", report)
	}
}

func TestBuildExecutionPlansOnlyIncludesAffectedPackages(t *testing.T) {
	root := t.TempDir()
	packages := []Package{
		{Path: ".", Manager: "go", Manifest: "go.mod", Affected: true},
		{Path: "apps/web", Manager: "npm", Manifest: "apps/web/package.json", Affected: false},
	}
	plans := BuildExecutionPlans(root, packages)
	if len(plans) != 1 {
		t.Fatalf("plans=%+v", plans)
	}
	plan := plans[0]
	if plan.PackagePath != "." || plan.Directory != root || len(plan.Commands) != 2 {
		t.Fatalf("unexpected go plan=%+v", plan)
	}
	if plan.Commands[0][0] != "go" || plan.Commands[0][1] != "test" || plan.Reason == "" {
		t.Fatalf("plan lacks bounded command metadata=%+v", plan)
	}
}

func TestBuildExecutionPlansMarksUnavailableToolsWithoutExecuting(t *testing.T) {
	plans := BuildExecutionPlans(t.TempDir(), []Package{
		{Path: "service", Manager: "unknown", Affected: true},
	})
	if len(plans) != 1 || plans[0].Available || plans[0].Reason == "" || len(plans[0].Commands) != 0 {
		t.Fatalf("unexpected unsupported-manager plan=%+v", plans)
	}
}
