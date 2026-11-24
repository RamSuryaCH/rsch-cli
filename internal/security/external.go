package security

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
)

type ExternalFinding struct {
	ID       string `json:"id,omitempty"`
	Package  string `json:"package,omitempty"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message,omitempty"`
	Fix      string `json:"fix,omitempty"`
}

// ExternalResult is an explicit observation from an optional third-party
// scanner. Unavailable scanners are never represented as passing.
type ExternalResult struct {
	Name      string            `json:"name"`
	Command   []string          `json:"command"`
	Available bool              `json:"available"`
	Passed    bool              `json:"passed"`
	ExitCode  int               `json:"exitCode,omitempty"`
	Findings  []ExternalFinding `json:"findings,omitempty"`
	Output    string            `json:"output,omitempty"`
	Reason    string            `json:"reason,omitempty"`
}

type externalScanner struct {
	Name       string
	Binary     string
	Arguments  []string
	Applicable func(string) bool
}

var externalScanners = map[string]externalScanner{
	"govulncheck": {
		Name:       "govulncheck",
		Binary:     "govulncheck",
		Arguments:  []string{"-json", "./..."},
		Applicable: hasFile("go.mod"),
	},
	"npm-audit": {
		Name:       "npm-audit",
		Binary:     "npm",
		Arguments:  []string{"audit", "--json"},
		Applicable: hasAnyFile("package.json", "package-lock.json", "npm-shrinkwrap.json"),
	},
	"pip-audit": {
		Name:       "pip-audit",
		Binary:     "pip-audit",
		Arguments:  []string{"--format=json"},
		Applicable: hasAnyFile("requirements.txt", "requirements-dev.txt", "pyproject.toml", "poetry.lock", "Pipfile.lock"),
	},
	"trivy": {
		Name:       "trivy",
		Binary:     "trivy",
		Arguments:  []string{"fs", "--format", "json", "--scanners", "vuln,secret,misconfig", "--exit-code", "1", "."},
		Applicable: func(root string) bool { return strings.TrimSpace(root) != "" },
	},
}

// ExternalScannerNames returns the stable allowlist accepted by CLI/API
// callers. A caller must opt into a scanner; security commands never launch
// network-capable tools implicitly.
func ExternalScannerNames() []string {
	names := make([]string, 0, len(externalScanners))
	for name := range externalScanners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RunExternal executes only explicitly requested scanners and reports missing
// binaries or inapplicable manifests as unavailable observations.
func RunExternal(ctx context.Context, root string, names []string) ([]ExternalResult, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("external scanner root is required")
	}
	results := make([]ExternalResult, 0, len(names))
	seen := map[string]bool{}
	for _, rawName := range names {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		scanner, ok := externalScanners[name]
		if !ok {
			return nil, fmt.Errorf("unsupported external scanner %q; supported scanners: %s", name, strings.Join(ExternalScannerNames(), ", "))
		}
		command := append([]string{scanner.Binary}, scanner.Arguments...)
		result := ExternalResult{Name: scanner.Name, Command: command}
		if !scanner.Applicable(root) {
			result.Reason = "scanner is not applicable to the detected repository manifests"
			results = append(results, result)
			continue
		}
		if _, err := exec.LookPath(scanner.Binary); err != nil {
			result.Reason = scanner.Binary + " is unavailable"
			results = append(results, result)
			continue
		}
		run, cancel := context.WithTimeout(ctx, scannerTimeout(name))
		output, err := process.RunOutput(run, process.Spec{Argv: command, Directory: root})
		cancel()
		result.Available = true
		result.ExitCode = output.ExitCode
		result.Findings = parseExternalFindings(name, output.Output)
		result.Output = redactExternalOutput(output.Output)
		result.Passed = err == nil
		// Trivy defaults to a successful exit even when findings exist.
		// Keep normalized findings authoritative if a wrapper overrides the
		// requested exit code; the flag also covers secrets/misconfigurations.
		if name == "trivy" && len(result.Findings) > 0 {
			result.Passed = false
		}
		if err != nil && result.Output == "" {
			result.Reason = err.Error()
		}
		results = append(results, result)
	}
	return results, nil
}

func scannerTimeout(name string) (timeout time.Duration) {
	return 2 * time.Minute
}

func redactExternalOutput(output []byte) string {
	const maxOutput = 64 * 1024
	value := RedactText(string(output))
	if len(value) > maxOutput {
		return value[:maxOutput] + "\n[external scanner output truncated]"
	}
	return value
}

func parseExternalFindings(name string, output []byte) []ExternalFinding {
	switch name {
	case "npm-audit":
		return parseNPMFindings(output)
	case "pip-audit":
		return parsePipFindings(output)
	case "trivy":
		return parseTrivyFindings(output)
	case "govulncheck":
		return parseGovulncheckFindings(output)
	default:
		return nil
	}
}

func parseNPMFindings(output []byte) []ExternalFinding {
	var report struct {
		Vulnerabilities map[string]struct {
			Severity string            `json:"severity"`
			Via      []json.RawMessage `json:"via"`
			Fix      json.RawMessage   `json:"fixAvailable"`
		} `json:"vulnerabilities"`
	}
	if json.Unmarshal(output, &report) != nil {
		return nil
	}
	findings := make([]ExternalFinding, 0, len(report.Vulnerabilities))
	for packageName, vulnerability := range report.Vulnerabilities {
		finding := ExternalFinding{Package: packageName, Severity: vulnerability.Severity}
		for _, raw := range vulnerability.Via {
			var id string
			if json.Unmarshal(raw, &id) == nil && id != "" {
				finding.ID = id
				break
			}
			var detail struct {
				Source json.RawMessage `json:"source"`
				Title  string          `json:"title"`
				URL    string          `json:"url"`
			}
			if json.Unmarshal(raw, &detail) == nil {
				finding.ID = firstNonEmpty(rawJSONText(detail.Source), detail.URL)
				finding.Message = detail.Title
			}
		}
		if len(vulnerability.Fix) > 0 && string(vulnerability.Fix) != "false" {
			finding.Fix = string(vulnerability.Fix)
		}
		findings = append(findings, finding)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Package < findings[j].Package })
	return findings
}

func parsePipFindings(output []byte) []ExternalFinding {
	var report []struct {
		Name            string `json:"name"`
		Vulnerabilities []struct {
			ID          string   `json:"id"`
			Description string   `json:"description"`
			FixVersions []string `json:"fix_versions"`
		} `json:"vulns"`
	}
	if json.Unmarshal(output, &report) != nil {
		return nil
	}
	findings := make([]ExternalFinding, 0)
	for _, packageReport := range report {
		for _, vulnerability := range packageReport.Vulnerabilities {
			findings = append(findings, ExternalFinding{ID: vulnerability.ID, Package: packageReport.Name, Message: vulnerability.Description, Fix: strings.Join(vulnerability.FixVersions, ", ")})
		}
	}
	return findings
}

func parseTrivyFindings(output []byte) []ExternalFinding {
	var report struct {
		Results []struct {
			Target          string `json:"Target"`
			Vulnerabilities []struct {
				ID               string `json:"VulnerabilityID"`
				Package          string `json:"PkgName"`
				Severity         string `json:"Severity"`
				Title            string `json:"Title"`
				Description      string `json:"Description"`
				InstalledVersion string `json:"InstalledVersion"`
				FixedVersion     string `json:"FixedVersion"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if json.Unmarshal(output, &report) != nil {
		return nil
	}
	findings := make([]ExternalFinding, 0)
	for _, result := range report.Results {
		for _, vulnerability := range result.Vulnerabilities {
			packageName := vulnerability.Package
			if result.Target != "" && packageName != "" {
				packageName += "@" + result.Target
			}
			message := firstNonEmpty(vulnerability.Title, vulnerability.Description)
			fix := vulnerability.FixedVersion
			if fix == "" {
				fix = vulnerability.InstalledVersion
			}
			findings = append(findings, ExternalFinding{ID: vulnerability.ID, Package: packageName, Severity: vulnerability.Severity, Message: message, Fix: fix})
		}
	}
	return findings
}

func parseGovulncheckFindings(output []byte) []ExternalFinding {
	findings := make([]ExternalFinding, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		finding, ok := govulncheckFinding(event)
		if ok {
			findings = append(findings, finding)
		}
	}
	return findings
}

func govulncheckFinding(event map[string]any) (ExternalFinding, bool) {
	for _, key := range []string{"finding", "osv"} {
		if nested, ok := event[key].(map[string]any); ok {
			finding := ExternalFinding{ID: stringValue(nested, "id", "osv"), Package: stringValue(event, "package", "module"), Severity: stringValue(nested, "severity"), Message: stringValue(nested, "summary", "details", "description")}
			if finding.ID != "" || finding.Package != "" || finding.Message != "" {
				return finding, true
			}
		}
	}
	return ExternalFinding{}, false
}

func stringValue(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func rawJSONText(value json.RawMessage) string {
	if len(value) == 0 || string(value) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(value, &number) == nil {
		return number.String()
	}
	return ""
}

func hasFile(name string) func(string) bool {
	return func(root string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
}

func hasAnyFile(names ...string) func(string) bool {
	return func(root string) bool {
		for _, name := range names {
			if hasFile(name)(root) {
				return true
			}
		}
		return false
	}
}
