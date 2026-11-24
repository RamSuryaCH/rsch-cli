package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/experiment"
	"github.com/spf13/cobra"
)

func TestExperimentCommandsRejectPrecancelledRequests(t *testing.T) {
	for _, original := range []*cobra.Command{experimentStartCmd, experimentCompareCmd, experimentCloseCmd} {
		t.Run(original.Name(), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			command := &cobra.Command{Use: original.Use, RunE: original.RunE}
			command.SetContext(ctx)
			if err := command.RunE(command, []string{"cancelled"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled %s returned %v", original.Name(), err)
			}
			if _, err := os.Stat(filepath.Join(root, ".rsch")); !os.IsNotExist(err) {
				t.Fatalf("cancelled request changed workspace state: %v", err)
			}
		})
	}
}

func TestExperimentCommandsCancelActiveGitInspection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git fixture requires a POSIX shell")
	}
	for _, original := range []*cobra.Command{experimentCompareCmd, experimentCloseCmd} {
		t.Run(original.Name(), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			record := experiment.Record{ID: "active", Worktree: filepath.Join(root, "worktree"), Base: "HEAD", Status: "active"}
			if err := os.Mkdir(record.Worktree, 0o700); err != nil {
				t.Fatal(err)
			}
			recordPath := filepath.Join(root, ".rsch", "experiments", "active.json")
			if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(recordPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := `#!/bin/sh
if [ "$3" = rev-parse ]; then
  : > "$RSCH_TEST_EXPERIMENT_READY"
  sleep 5
  printf 'true\n'
elif [ "$3" = worktree ]; then
  : > "$RSCH_TEST_EXPERIMENT_MUTATED"
fi
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			readyPath := filepath.Join(root, "ready")
			mutatedPath := filepath.Join(root, "removed")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RSCH_TEST_EXPERIMENT_READY", readyPath)
			t.Setenv("RSCH_TEST_EXPERIMENT_MUTATED", mutatedPath)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ready := make(chan bool, 1)
			go func() {
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						ready <- false
						return
					case <-ticker.C:
						if _, err := os.Stat(readyPath); err == nil {
							cancel()
							ready <- true
							return
						}
					}
				}
			}()
			command := &cobra.Command{Use: original.Use, RunE: original.RunE}
			var stdout, stderr bytes.Buffer
			exit := executeCommand(ctx, command, []string{"active"}, strings.NewReader(""), &stdout, &stderr)
			cancel()
			if !<-ready {
				t.Fatal("Git inspection did not reach the cancellation point")
			}
			if exit != 130 {
				t.Fatalf("cancelled %s exit = %d; stderr = %q", original.Name(), exit, stderr.String())
			}
			current, err := os.ReadFile(recordPath)
			if err != nil || !bytes.Equal(current, data) {
				t.Fatalf("cancelled experiment changed record: %s, %v", current, err)
			}
			for _, path := range []string{mutatedPath, filepath.Join(root, ".rsch", "evidence")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("cancelled experiment produced side effect %s: %v", path, err)
				}
			}
		})
	}
}
