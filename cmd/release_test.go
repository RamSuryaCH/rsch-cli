package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rschevidence "github.com/opencode-ai/opencode/internal/evidence"
	"github.com/opencode-ai/opencode/internal/release"
	"github.com/spf13/cobra"
)

func TestReleaseMatrixDistinguishesMissingAndCorruptEvidence(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		name := "missing"
		if corrupt {
			name = "corrupt"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			if corrupt {
				writeCorruptReleaseMatrix(t, root)
			}
			command := &cobra.Command{Use: "matrix", RunE: releaseMatrixCmd.RunE}
			var output bytes.Buffer
			command.SetOut(&output)
			err := command.RunE(command, nil)
			if (err != nil) != corrupt {
				t.Fatalf("RunE error = %v, corrupt = %v", err, corrupt)
			}
			if corrupt && output.Len() != 0 {
				t.Fatalf("corrupt evidence reported as missing: %q", output.String())
			}
			if !corrupt && output.String() != "no release evidence recorded\n" {
				t.Fatalf("unexpected missing matrix output: %q", output.String())
			}
		})
	}
}

func TestReleaseCheckPreservesCorruptEvidence(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	path := writeCorruptReleaseMatrix(t, root)
	// No compiler is available: the command must reject corrupt evidence
	// before attempting a build or replacing it with new failure evidence.
	t.Setenv("PATH", t.TempDir())
	command := &cobra.Command{Use: "check", RunE: releaseCheckCmd.RunE}
	command.SetContext(context.Background())
	if err := command.RunE(command, []string{"linux", "amd64"}); err == nil {
		t.Fatal("corrupt matrix should fail the check")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{invalid evidence" {
		t.Fatalf("existing matrix was overwritten: %q", data)
	}
}

func TestReleaseCheckAllCancellationCannotReportSuccess(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		exit int
		code string
	}{
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			exit: 130,
			code: "INTERRUPTED",
		},
		{
			name: "deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			exit: 124,
			code: "TIMED_OUT",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			ctx, cancel := test.ctx()
			defer cancel()
			command := &cobra.Command{Use: "check-all", RunE: releaseCheckAllCmd.RunE}
			var stdout, stderr bytes.Buffer
			if exit := executeCommand(ctx, command, nil, strings.NewReader(""), &stdout, &stderr); exit != test.exit {
				t.Fatalf("exit = %d, want %d; stderr = %q", exit, test.exit, stderr.String())
			}
			if envelope := decodeCLIError(t, stderr.Bytes()); envelope.Code != test.code {
				t.Fatalf("error code = %q, want %q", envelope.Code, test.code)
			}
			var matrix release.Matrix
			if err := json.Unmarshal(stdout.Bytes(), &matrix); err != nil {
				t.Fatalf("release output must remain structured JSON: %v", err)
			}
			if len(matrix.Evidence) != len(release.SupportedTargets()) {
				t.Fatalf("missing cancellation evidence: %+v", matrix)
			}
			for _, item := range matrix.Evidence {
				if item.Built || item.Status != "cancelled" {
					t.Fatalf("cancelled target reported as built: %+v", item)
				}
			}
			store, err := rschevidence.New(root)
			if err != nil {
				t.Fatal(err)
			}
			records, err := store.List(0)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Status != rschevidence.Failed {
				t.Fatalf("cancelled release recorded as successful evidence: %+v", records)
			}
		})
	}
}

func writeCorruptReleaseMatrix(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, ".rsch", "release", "matrix.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{invalid evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
