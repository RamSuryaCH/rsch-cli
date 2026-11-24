// Package security provides deterministic local security checks that can run
// without a model or network connection.
package security

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type Severity string

const (
	Low      Severity = "low"
	Medium   Severity = "medium"
	High     Severity = "high"
	Critical Severity = "critical"
)

type Finding struct {
	Rule           string   `json:"rule"`
	Category       string   `json:"category"`
	Severity       Severity `json:"severity"`
	File           string   `json:"file"`
	Line           int      `json:"line"`
	Evidence       string   `json:"evidence"`
	Impact         string   `json:"impact"`
	Recommendation string   `json:"recommendation"`
	Status         string   `json:"status"`
	Fingerprint    string   `json:"fingerprint"`
}

var secretRules = []struct {
	name           string
	severity       Severity
	pattern        *regexp.Regexp
	recommendation string
}{
	{"private-key", Critical, regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`), "Remove the key, rotate it, and use an approved secret store."},
	{"aws-access-key", Critical, regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "Rotate the credential and remove it from source control."},
	{"generic-secret-assignment", High, regexp.MustCompile(`(?i)["']?\b(api[_-]?key|secret|token|password)\b["']?\s*[:=]\s*["'][^"']{12,}["']`), "Move the secret to environment or managed secret storage."},
}

const credentialAssignment = `(?i)(["']?\b(?:api[_-]?key|secret|token|password)\b["']?\s*[:=]\s*`

var redactionRules = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?is)-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----.*?(?:-----END (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----|$)`), "[REDACTED]"},
	// Escaped quotes are part of the credential, not its closing delimiter.
	{regexp.MustCompile(credentialAssignment + `")(?:\\.|[^"\\])*(")`), `${1}[REDACTED]${2}`},
	{regexp.MustCompile(credentialAssignment + `')(?:\\.|[^'\\])*(')`), `${1}[REDACTED]${2}`},
	{regexp.MustCompile(credentialAssignment + `)(?:\[REDACTED\]|[^\s"',;}\]]+)`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(\bauthorization["']?\s*[:=]\s*["']?\s*(?:bearer|basic|token)\s+)([A-Za-z0-9._~+/-]+=*)`), `${1}[REDACTED]`},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), "[REDACTED]"},
}

// RedactText removes recognizable credential material before text is logged,
// persisted, or sent to an external integration.
func RedactText(value string) string {
	for _, rule := range redactionRules {
		value = rule.pattern.ReplaceAllString(value, rule.replacement)
	}
	return value
}

func ScanFile(path string) ([]Finding, error) {
	data, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	return scanReader(path, data)
}

func scanReader(path string, reader io.Reader) ([]Finding, error) {
	var findings []Finding
	scanner := bufio.NewScanner(reader)
	// Minified configuration files routinely exceed Scanner's 64 KiB default.
	// Keep a finite per-line allocation while accepting a full 2 MiB file line.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024+1)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		for _, rule := range secretRules {
			if rule.pattern.MatchString(text) {
				sum := sha256.Sum256([]byte(path + ":" + strconv.Itoa(line) + ":" + rule.name))
				findings = append(findings, Finding{
					Rule:           rule.name,
					Category:       "secret-detection",
					Severity:       rule.severity,
					File:           path,
					Line:           line,
					Evidence:       redact(text),
					Impact:         "credential material may be exposed to source control, logs, or downstream integrations",
					Recommendation: rule.recommendation,
					Status:         "open",
					Fingerprint:    hex.EncodeToString(sum[:]),
				})
			}
		}
	}
	return findings, scanner.Err()
}
func redact(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return "[redacted]"
	}
	return value[:4] + "…[redacted]…" + value[len(value)-4:]
}
