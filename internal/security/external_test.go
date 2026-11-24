package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalScannerNamesAreStableAndAllowlisted(t *testing.T) {
	names := ExternalScannerNames()
	if len(names) != 4 || names[0] != "govulncheck" || names[1] != "npm-audit" || names[2] != "pip-audit" || names[3] != "trivy" {
		t.Fatalf("unexpected scanner names: %v", names)
	}
	if _, err := RunExternal(context.Background(), t.TempDir(), []string{"unknown"}); err == nil {
		t.Fatal("unknown scanner was accepted")
	}
}

func TestExternalScannerReportsUnavailableWithoutPassing(t *testing.T) {
	root := t.TempDir()
	results, err := RunExternal(context.Background(), root, []string{"npm-audit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Available && results[0].Passed {
		t.Fatalf("unavailable scanner was treated as passing: %+v", results)
	}
}

func TestExternalScannerRedactsOutputAndCapturesPass(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := filepath.Join(bin, "govulncheck")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'token: \"super-secret-value\"'\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", bin+string(os.PathListSeparator)+originalPath); err != nil {
		t.Fatal(err)
	}
	defer os.Setenv("PATH", originalPath)
	results, err := RunExternal(context.Background(), root, []string{"govulncheck"})
	if err != nil || len(results) != 1 {
		t.Fatalf("scanner result=%+v err=%v", results, err)
	}
	if !results[0].Available || !results[0].Passed || strings.Contains(results[0].Output, "super-secret-value") {
		t.Fatalf("scanner output was not observed/redacted: %+v", results[0])
	}
}

func TestExternalFindingParsersNormalizeVendorReports(t *testing.T) {
	npm := parseExternalFindings("npm-audit", []byte(`{"vulnerabilities":{"lodash":{"severity":"high","via":[{"source":12345,"title":"prototype pollution","url":"https://example.test/advisory"}],"fixAvailable":true}}}`))
	if len(npm) != 1 || npm[0].Package != "lodash" || npm[0].Severity != "high" || npm[0].Message != "prototype pollution" {
		t.Fatalf("npm findings=%+v", npm)
	}
	pip := parseExternalFindings("pip-audit", []byte(`[{"name":"requests","vulns":[{"id":"PYSEC-1","description":"bad request","fix_versions":["2.32.0"]}]}]`))
	if len(pip) != 1 || pip[0].ID != "PYSEC-1" || pip[0].Package != "requests" || pip[0].Fix != "2.32.0" {
		t.Fatalf("pip findings=%+v", pip)
	}
	trivy := parseExternalFindings("trivy", []byte(`{"Results":[{"Target":"alpine:latest","Vulnerabilities":[{"VulnerabilityID":"CVE-1","PkgName":"openssl","Severity":"CRITICAL","Title":"openssl issue","FixedVersion":"1.2"}]}]}`))
	if len(trivy) != 1 || trivy[0].ID != "CVE-1" || trivy[0].Severity != "CRITICAL" || trivy[0].Fix != "1.2" {
		t.Fatalf("trivy findings=%+v", trivy)
	}
	govuln := parseExternalFindings("govulncheck", []byte("{\"finding\":{\"osv\":\"GO-1\",\"summary\":\"stdlib issue\"},\"package\":\"example.test/pkg\"}\n"))
	if len(govuln) != 1 || govuln[0].ID != "GO-1" || govuln[0].Package != "example.test/pkg" {
		t.Fatalf("govulncheck findings=%+v", govuln)
	}
}

func TestTrivyFindingsCannotPassWithZeroExitStatus(t *testing.T) {
	bin := t.TempDir()
	report := `{"Results":[{"Target":"go.mod","Vulnerabilities":[{"VulnerabilityID":"TEST-1","PkgName":"example.test/package","Severity":"HIGH"}]}]}`
	script := "#!/bin/sh\nprintf '%s\\n' '" + report + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "trivy"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	results, err := RunExternal(context.Background(), t.TempDir(), []string{"trivy"})
	if err != nil || len(results) != 1 {
		t.Fatalf("scanner result=%+v err=%v", results, err)
	}
	if !results[0].Available || results[0].Passed || results[0].ExitCode != 0 || len(results[0].Findings) != 1 {
		t.Fatalf("scanner findings were treated as passing: %+v", results[0])
	}
}

func TestTrivyRequestsFailureStatusForAllFindingCategories(t *testing.T) {
	bin := t.TempDir()
	// Trivy normally exits zero even with secret or misconfiguration findings.
	// Its exit-code option covers categories not normalized by our parser.
	script := "#!/bin/sh\ncase \" $* \" in\n*' --exit-code 1 '*) exit 1 ;;\n*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "trivy"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	results, err := RunExternal(context.Background(), t.TempDir(), []string{"trivy"})
	if err != nil || len(results) != 1 {
		t.Fatalf("scanner result=%+v err=%v", results, err)
	}
	if !results[0].Available || results[0].Passed || results[0].ExitCode != 1 {
		t.Fatalf("scanner did not request failing exit status for findings: %+v", results[0])
	}
}
