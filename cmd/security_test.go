package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/security"
	"github.com/spf13/cobra"
)

func TestSecurityReportsExternalResultsWithoutLocalFindings(t *testing.T) {
	for _, test := range []struct {
		name       string
		manifest   bool
		exitStatus string
		want       string
		wantError  bool
	}{
		{name: "unavailable", want: "external govulncheck: unavailable (scanner is not applicable", wantError: true},
		{name: "passed", manifest: true, exitStatus: "0", want: "external govulncheck: passed"},
		{name: "failed", manifest: true, exitStatus: "1", want: "external govulncheck: failed", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if test.manifest {
				if runtime.GOOS == "windows" {
					t.Skip("scanner fixture requires a POSIX shell")
				}
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "govulncheck"), []byte("#!/bin/sh\nexit "+test.exitStatus+"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			command := securityTestCommand("text")
			var output bytes.Buffer
			command.SetOut(&output)
			err := command.RunE(command, nil)
			if (err != nil) != test.wantError {
				t.Fatalf("RunE error = %v, wantError = %v", err, test.wantError)
			}
			if !strings.Contains(output.String(), "no secret findings\n") || !strings.Contains(output.String(), test.want) {
				t.Fatalf("missing external scanner result in %q", output.String())
			}
		})
	}
}

func TestSecurityJSONKeepsExternalResultsStructured(t *testing.T) {
	t.Chdir(t.TempDir())
	command := securityTestCommand("json")
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.RunE(command, nil); err == nil {
		t.Fatal("unavailable scanner should fail")
	}
	var report struct {
		LocalFindings []security.Finding        `json:"localFindings"`
		External      []security.ExternalResult `json:"external"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON output %q: %v", output.String(), err)
	}
	if len(report.LocalFindings) != 0 || len(report.External) != 1 || report.External[0].Available {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func securityTestCommand(format string) *cobra.Command {
	command := &cobra.Command{Use: "security", RunE: securityCmd.RunE}
	command.SetContext(context.Background())
	command.Flags().String("format", format, "")
	command.Flags().StringSlice("external", []string{"govulncheck"}, "")
	return command
}
