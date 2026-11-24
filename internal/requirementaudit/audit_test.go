package requirementaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunProducesExplicitCoverageAndGaps(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".rsch", "product", "features"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"version":1,"features":[{"id":"event-bus","name":"Versioned events and replay log","status":"implementing","verification":["README.md"]}]}`
	if err := os.WriteFile(filepath.Join(root, ".rsch", "product", "features", "registry.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(root, "prompt.md")
	if err := os.WriteFile(prompt, []byte("# 14. CENTRAL EVENT SYSTEM\n# 99. UNMAPPED THING\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(prompt, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalSections != 2 || report.TrackedSections != 1 || report.UntrackedSections != 1 || report.EvidenceSections != 1 {
		t.Fatalf("report=%+v", report)
	}
	if err := Save(root, report); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch", "requirements", "audit.json")); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(report)
	if len(data) == 0 {
		t.Fatal("empty report")
	}
}

func TestRunUsesExplicitAliasesAndConditionalApplicability(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".rsch", "product", "features"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"version":1,"features":[{"id":"configuration-hierarchy","name":"Layered configuration","status":"implementing","verification":["README.md"]}]}`
	if err := os.WriteFile(filepath.Join(root, ".rsch", "product", "features", "registry.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(root, "prompt.md")
	if err := os.WriteFile(prompt, []byte("# 92. CONFIGURATION HIERARCHY\n# 39. MACOS SEATBELT SOURCE ATTRIBUTION\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(prompt, root)
	if err != nil {
		t.Fatal(err)
	}
	if report.TrackedSections != 1 || report.NotApplicableSections != 1 || report.UntrackedSections != 0 || report.EvidenceSections != 1 {
		t.Fatalf("report=%+v", report)
	}
}
