package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestVerifyRejectsUnknownGateBeforeRunningChecks(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	command := &cobra.Command{Use: "verify", RunE: verifyCmd.RunE}
	command.Flags().String("gate", "git-typo", "")
	var stdout, stderr bytes.Buffer
	exitCode := executeCommand(context.Background(), command, nil, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want usage error 2; stderr = %q", exitCode, stderr.String())
	}
	envelope := decodeCLIError(t, stderr.Bytes())
	if !envelope.Error || !strings.Contains(stderr.String(), "unsupported verification gate") {
		t.Fatalf("unexpected usage error: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected verification output: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch")); !os.IsNotExist(err) {
		t.Fatalf("invalid gate wrote workspace evidence: %v", err)
	}
}
