package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFileFindsAndRedactsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.go")
	fixture := `const ` + strings.Join([]string{"api", "Key"}, "") + ` = "` + strings.Repeat("1234567890", 2) + `"`
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	findings, err := ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || strings.Contains(findings[0].Evidence, "123456789012") {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if findings[0].Category != "secret-detection" || findings[0].Impact == "" || findings[0].Status != "open" || findings[0].Fingerprint == "" {
		t.Fatalf("finding lacks required security metadata: %+v", findings[0])
	}
}

func TestRedactTextRemovesSecretsFromStructuredText(t *testing.T) {
	text := `{"apiKey":"super-secret-value", "token":"abc"}`
	redacted := RedactText(text)
	if strings.Contains(redacted, "super-secret-value") || strings.Contains(redacted, "abc") {
		t.Fatalf("secret remained in redacted text: %s", redacted)
	}
}

func TestScanFileFindsJSONSecretsOnLongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	fixture := `{"padding":"` + strings.Repeat("x", 70*1024) + `","apiKey":"` + strings.Repeat("test-value", 3) + `"}`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := ScanFile(path)
	if err != nil || len(findings) != 1 || findings[0].Rule != "generic-secret-assignment" {
		t.Fatalf("JSON secret was not scanned: findings=%d err=%v", len(findings), err)
	}
}

func TestRedactTextHandlesEscapedAndUnquotedValues(t *testing.T) {
	for _, text := range []string{
		`{"token":"prefix\"sensitive-tail"}`,
		`password='prefix\'sensitive-tail'`,
		`API_KEY=sensitive-tail`,
		`{"Authorization":"Bearer sensitive-tail"}`,
		"-----BEGIN PRIVATE KEY-----\nsensitive-tail",
	} {
		redacted := RedactText(text)
		if strings.Contains(redacted, "sensitive-tail") {
			t.Fatalf("redaction left credential content in %q", redacted)
		}
		if repeated := RedactText(redacted); repeated != redacted {
			t.Fatalf("redaction must be idempotent: %q became %q", redacted, repeated)
		}
	}
}
