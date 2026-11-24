package release

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/process"
)

func TestSupportedTargetValidation(t *testing.T) {
	if err := ValidateTarget(Target{GOOS: "linux", GOARCH: "amd64"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTarget(Target{GOOS: "plan9", GOARCH: "amd64"}); err == nil {
		t.Fatal("expected unsupported target")
	}
}

func TestConfiguredPackagingMatchesTarget(t *testing.T) {
	if got := configuredPackaging(Target{GOOS: "windows", GOARCH: "amd64"}); got != "configured: zip" {
		t.Fatalf("windows packaging=%q", got)
	}
	if got := configuredPackaging(Target{GOOS: "linux", GOARCH: "arm64"}); got != "configured: tar.gz, deb, rpm" {
		t.Fatalf("linux packaging=%q", got)
	}
	if got := configuredPackaging(Target{GOOS: "darwin", GOARCH: "arm64"}); got != "configured: tar.gz" {
		t.Fatalf("darwin packaging=%q", got)
	}
}

func TestCheckMarksUnsupportedTargetWithoutBuilding(t *testing.T) {
	evidence := Check(context.Background(), t.TempDir(), Target{GOOS: "plan9", GOARCH: "amd64"})
	if evidence.Status != "unsupported" || evidence.Built {
		t.Fatalf("evidence=%+v", evidence)
	}
}

func TestValidatedTargetsOnlyAdvertisesSuccessfulBuildEvidence(t *testing.T) {
	root := t.TempDir()
	if _, err := ValidatedTargets(root); !os.IsNotExist(err) {
		t.Fatalf("expected missing matrix error, got %v", err)
	}
	if err := Save(root, Matrix{Version: 1, Evidence: []Evidence{
		{Target: Target{GOOS: "linux", GOARCH: "amd64"}, Status: "build-failed", Built: false},
		{Target: Target{GOOS: "windows", GOARCH: "amd64"}, Status: "built", Built: true},
		{Target: Target{GOOS: "darwin", GOARCH: "arm64"}, Status: "runtime-tested", Built: true},
	}}); err != nil {
		t.Fatal(err)
	}
	targets, err := ValidatedTargets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0] != (Target{GOOS: "darwin", GOARCH: "arm64"}) || targets[1] != (Target{GOOS: "windows", GOARCH: "amd64"}) {
		t.Fatalf("unexpected advertised targets: %+v", targets)
	}
}

func TestLastNonEmptyLineUsesVersionContractLine(t *testing.T) {
	for name, test := range map[string]struct {
		output string
		want   string
	}{
		"empty":       {output: "", want: ""},
		"version":     {output: runtimeProbeVersion + "\n", want: runtimeProbeVersion},
		"diagnostics": {output: "warning: optional tool unavailable\n" + runtimeProbeVersion + "\n", want: runtimeProbeVersion},
		"unknown":     {output: "warning\nunknown\n", want: "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := lastNonEmptyLine([]byte(test.output)); got != test.want {
				t.Fatalf("lastNonEmptyLine() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCheckNativeRequiresExpectedVersionOutput(t *testing.T) {
	if !process.Exists("go") {
		t.Skip("go executable is unavailable")
	}
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if err := ValidateTarget(target); err != nil {
		t.Skipf("native target is not in the release matrix: %v", err)
	}

	t.Run("expected", func(t *testing.T) {
		root := writeRuntimeProbeFixture(t, true)
		evidence := Check(context.Background(), root, target)
		if !evidence.Built || !evidence.RuntimeTested || evidence.Status != "runtime-tested" {
			t.Fatalf("evidence = %+v, want built and runtime-tested", evidence)
		}
		if evidence.ExpectedVersion != runtimeProbeVersion || evidence.ObservedVersion != runtimeProbeVersion {
			t.Fatalf("version evidence = expected %q observed %q", evidence.ExpectedVersion, evidence.ObservedVersion)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		root := writeRuntimeProbeFixture(t, false)
		evidence := Check(context.Background(), root, target)
		if !evidence.Built || evidence.RuntimeTested || evidence.Status != "built" {
			t.Fatalf("evidence = %+v, want built without runtime-tested", evidence)
		}
		if evidence.ObservedVersion != "unknown" || !strings.Contains(evidence.Output, "runtime version mismatch") {
			t.Fatalf("mismatch evidence = %+v", evidence)
		}
	})
}

func writeRuntimeProbeFixture(t *testing.T, useInjectedVersion bool) string {
	t.Helper()
	root := t.TempDir()
	versionDirectory := filepath.Join(root, "internal", "version")
	if err := os.MkdirAll(versionDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/opencode-ai/opencode\n\ngo 1.26.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDirectory, "version.go"), []byte("package version\n\nvar Version = \"dev\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainSource := "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tif len(os.Args) == 2 && os.Args[1] == \"--version\" { fmt.Println(\"unknown\") }\n}\n"
	if useInjectedVersion {
		mainSource = "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"github.com/opencode-ai/opencode/internal/version\"\n)\n\nfunc main() {\n\tif len(os.Args) == 2 && os.Args[1] == \"--version\" { fmt.Println(version.Version) }\n}\n"
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(mainSource), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
