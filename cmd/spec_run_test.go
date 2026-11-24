package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/opencode-ai/opencode/internal/spec"
	"github.com/spf13/cobra"
)

func TestSpecRunDoesNotExecuteCancelledPlan(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	manager := spec.NewManager(root)
	if _, err := manager.New("cancelled-plan"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "executed")
	argv := []string{"touch", marker}
	if runtime.GOOS == "windows" {
		argv = []string{"cmd.exe", "/c", "type", "nul", ">", marker}
	}
	if err := manager.SavePlan("cancelled-plan", spec.Plan{Version: 1, Tasks: []spec.PlanTask{{ID: "one", Title: "local fixture", Command: argv}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := &cobra.Command{Use: "run", RunE: specRunCmd.RunE}
	command.SetContext(ctx)
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.RunE(command, []string{"cancelled-plan"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled plan returned %v", err)
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("plan output is not structured JSON: %q", stdout.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("cancelled plan executed task: %v", err)
	}
}
