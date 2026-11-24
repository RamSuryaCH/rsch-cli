// Package release records evidence for cross-platform builds without claiming
// runtime support that has not been validated.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/sandbox"
)

const (
	runtimeProbeVersion = "rsch-release-runtime-probe"
	runtimeProbeSymbol  = "github.com/opencode-ai/opencode/internal/version.Version"
)

type Target struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}
type Evidence struct {
	Target             Target    `json:"target"`
	Platform           string    `json:"platform,omitempty"`
	Runtime            string    `json:"runtime,omitempty"`
	NativeDependencies string    `json:"nativeDependencies,omitempty"`
	Sandbox            string    `json:"sandbox,omitempty"`
	Packaging          string    `json:"packaging,omitempty"`
	Status             string    `json:"status"`
	Built              bool      `json:"built"`
	RuntimeTested      bool      `json:"runtimeTested"`
	ExpectedVersion    string    `json:"expectedVersion,omitempty"`
	ObservedVersion    string    `json:"observedVersion,omitempty"`
	Output             string    `json:"output,omitempty"`
	CheckedAt          time.Time `json:"checkedAt"`
}
type Matrix struct {
	Version     int        `json:"version"`
	GeneratedAt time.Time  `json:"generatedAt,omitempty"`
	Targets     []Target   `json:"targets,omitempty"`
	Evidence    []Evidence `json:"evidence"`
}

func SupportedTargets() []Target {
	return []Target{{GOOS: "darwin", GOARCH: "arm64"}, {GOOS: "darwin", GOARCH: "amd64"}, {GOOS: "linux", GOARCH: "amd64"}, {GOOS: "linux", GOARCH: "arm64"}, {GOOS: "windows", GOARCH: "amd64"}, {GOOS: "windows", GOARCH: "arm64"}}
}

// ValidatedTargets returns only targets with persisted successful build
// evidence. Declared targets remain available to Check and ValidateTarget,
// but are not advertised to users until a real build has succeeded.
func ValidatedTargets(root string) ([]Target, error) {
	matrix, err := Load(root)
	if err != nil {
		return nil, err
	}
	validated := make(map[Target]bool, len(matrix.Evidence))
	for _, evidence := range matrix.Evidence {
		if evidence.Built && (evidence.Status == "built" || evidence.Status == "runtime-tested") {
			validated[evidence.Target] = true
		}
	}
	ordered := make([]Target, 0, len(validated))
	for _, target := range SupportedTargets() {
		if validated[target] {
			ordered = append(ordered, target)
		}
	}
	return ordered, nil
}
func Check(ctx context.Context, root string, target Target) Evidence {
	cgo := os.Getenv("CGO_ENABLED")
	if cgo == "" {
		cgo = "default"
	}
	evidence := Evidence{Target: target, Platform: target.GOOS, Runtime: target.GOOS + "/" + target.GOARCH, NativeDependencies: "CGO_ENABLED=" + cgo, Sandbox: "not-runtime-tested", Packaging: configuredPackaging(target), Status: "unsupported", CheckedAt: time.Now().UTC()}
	if target.GOOS == "" || target.GOARCH == "" {
		evidence.Output = "target GOOS and GOARCH are required"
		return evidence
	}
	if err := ValidateTarget(target); err != nil {
		evidence.Output = err.Error()
		return evidence
	}
	if !process.Exists("go") {
		evidence.Status = "required-but-missing"
		evidence.Output = "go executable is unavailable"
		return evidence
	}
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"go", "build", "./..."}, Directory: root, Environment: map[string]string{"GOOS": target.GOOS, "GOARCH": target.GOARCH}})
	evidence.Built = err == nil
	if evidence.Built && target.GOOS == runtime.GOOS && target.GOARCH == runtime.GOARCH {
		sandboxCapability := sandbox.Probe(ctx, root)
		evidence.Sandbox = string(sandboxCapability.State)
		temporary, tempErr := os.MkdirTemp("", "rsch-release-")
		if tempErr == nil {
			defer os.RemoveAll(temporary)
			binary := filepath.Join(temporary, "rsch")
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			evidence.ExpectedVersion = runtimeProbeVersion
			runtimeResult, runtimeErr := process.RunOutput(ctx, process.Spec{Argv: []string{"go", "build", "-trimpath", "-ldflags", "-X " + runtimeProbeSymbol + "=" + runtimeProbeVersion, "-o", binary, "."}, Directory: root, Environment: map[string]string{"GOOS": target.GOOS, "GOARCH": target.GOARCH}})
			if runtimeErr == nil {
				versionResult, runErr := process.RunOutput(ctx, process.Spec{Argv: []string{binary, "--version"}})
				evidence.ObservedVersion = lastNonEmptyLine(versionResult.Output)
				if runErr == nil && evidence.ObservedVersion == evidence.ExpectedVersion {
					evidence.RuntimeTested = true
					evidence.Output = strings.TrimSpace(strings.Join([]string{string(versionResult.Output), string(runtimeResult.Output)}, "\n"))
				} else if runErr != nil {
					evidence.Output = strings.TrimSpace(string(runErr.Error()) + "\n" + string(versionResult.Output))
				} else {
					evidence.Output = fmt.Sprintf("runtime version mismatch: expected %q, observed %q", evidence.ExpectedVersion, evidence.ObservedVersion)
				}
			} else {
				evidence.Output = strings.TrimSpace(string(runtimeResult.Output))
			}
		}
	}
	if evidence.Built {
		evidence.Status = "built"
		if evidence.RuntimeTested {
			evidence.Status = "runtime-tested"
		}
	} else if evidence.Output == "" {
		evidence.Status = "build-failed"
	} else {
		evidence.Status = "build-failed"
	}
	if evidence.Output == "" {
		evidence.Output = strings.TrimSpace(string(result.Output))
	}
	return evidence
}

// lastNonEmptyLine extracts the version line while allowing diagnostics written
// to stderr before the command's stdout. RunOutput intentionally combines both
// streams, so the final non-empty line is the CLI version contract.
func lastNonEmptyLine(output []byte) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

// configuredPackaging describes the formats declared by the GoReleaser
// configuration for a target. It is deliberately separate from Built and
// RuntimeTested: a configured archive format is not evidence that packaging
// was actually produced on this host.
func configuredPackaging(target Target) string {
	switch target.GOOS {
	case "windows":
		return "configured: zip"
	case "linux":
		return "configured: tar.gz, deb, rpm"
	case "darwin":
		return "configured: tar.gz"
	default:
		return "unconfigured"
	}
}

// CheckAll produces one explicit evidence record for every declared release
// target. It never marks a target runtime-tested merely because it cross-built.
func CheckAll(ctx context.Context, root string) Matrix {
	targets := SupportedTargets()
	evidence := make([]Evidence, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			evidence = append(evidence, Evidence{Target: target, Status: "cancelled", Output: err.Error(), CheckedAt: time.Now().UTC()})
			continue
		}
		evidence = append(evidence, Check(ctx, root, target))
	}
	return Matrix{Version: 1, GeneratedAt: time.Now().UTC(), Targets: targets, Evidence: evidence}
}
func Save(root string, matrix Matrix) error {
	if matrix.Version == 0 {
		matrix.Version = 1
	}
	path := filepath.Join(root, ".rsch", "release", "matrix.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(matrix, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
func Load(root string) (Matrix, error) {
	data, err := os.ReadFile(filepath.Join(root, ".rsch", "release", "matrix.json"))
	if err != nil {
		return Matrix{}, err
	}
	var matrix Matrix
	if err := json.Unmarshal(data, &matrix); err != nil {
		return Matrix{}, err
	}
	return matrix, nil
}
func ValidateTarget(target Target) error {
	for _, supported := range SupportedTargets() {
		if target == supported {
			return nil
		}
	}
	return fmt.Errorf("unsupported release target %s/%s", target.GOOS, target.GOARCH)
}
