package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplainInstructionsReportsHierarchyAndOverrides(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repository, "services", "api")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	rootInstructions := filepath.Join(repository, "AGENTS.md")
	shadowed := filepath.Join(target, "AGENTS.md")
	targetOverride := filepath.Join(target, "AGENTS.override.md")
	for path, content := range map[string]string{
		rootInstructions: "root",
		shadowed:         "shadowed",
		targetOverride:   "target override",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	report, err := explainInstructions(target)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	canonicalTarget := filepath.Join(canonicalRepository, "services", "api")
	if report.WorkingDirectory != canonicalTarget {
		t.Fatalf("working directory = %q, want %q", report.WorkingDirectory, canonicalTarget)
	}
	if report.Precedence != instructionPrecedence {
		t.Fatalf("precedence = %q", report.Precedence)
	}
	if len(report.Instructions) != 2 {
		t.Fatalf("instructions = %+v", report.Instructions)
	}
	if first := report.Instructions[0]; first.Order != 1 || first.Path != filepath.Join(canonicalRepository, "AGENTS.md") || first.Override {
		t.Fatalf("root instruction = %+v", first)
	}
	if second := report.Instructions[1]; second.Order != 2 || second.Path != filepath.Join(canonicalTarget, "AGENTS.override.md") || !second.Override {
		t.Fatalf("target instruction = %+v", second)
	}
	for _, instruction := range report.Instructions {
		if instruction.Path == filepath.Join(canonicalTarget, "AGENTS.md") {
			t.Fatalf("shadowed AGENTS.md was selected: %+v", report.Instructions)
		}
	}
}

func TestWriteInstructionExplanationSupportsTextAndJSON(t *testing.T) {
	report := instructionExplanation{
		WorkingDirectory: "/workspace/service",
		Precedence:       instructionPrecedence,
		Instructions: []instructionSelection{
			{Order: 1, Path: "/workspace/AGENTS.md", Directory: "/workspace"},
			{Order: 2, Path: "/workspace/service/AGENTS.override.md", Directory: "/workspace/service", Override: true},
		},
	}

	var textOutput bytes.Buffer
	if err := writeInstructionExplanation(&textOutput, report, false); err != nil {
		t.Fatal(err)
	}
	text := textOutput.String()
	for _, expected := range []string{"later files take precedence", "1. [AGENTS.md]", "2. [AGENTS.override.md]"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("text output %q does not contain %q", text, expected)
		}
	}

	var jsonOutput bytes.Buffer
	if err := writeInstructionExplanation(&jsonOutput, report, true); err != nil {
		t.Fatal(err)
	}
	var decoded instructionExplanation
	if err := json.Unmarshal(jsonOutput.Bytes(), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v", err)
	}
	if decoded.WorkingDirectory != report.WorkingDirectory || len(decoded.Instructions) != 2 || !decoded.Instructions[1].Override {
		t.Fatalf("decoded report = %+v", decoded)
	}
}

func TestInstructionsExplainArgumentContract(t *testing.T) {
	if err := instructionsCmd.Args(instructionsCmd, []string{"unexpected"}); err == nil {
		t.Fatal("instructions accepted an unexpected argument")
	}
	if err := instructionsExplainCmd.Args(instructionsExplainCmd, []string{"one", "two"}); err == nil {
		t.Fatal("instructions explain accepted more than one directory")
	}
}
