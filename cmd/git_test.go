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

	"github.com/opencode-ai/opencode/internal/review"
	"github.com/spf13/cobra"
)

func TestGitReviewReturnsFailureWithUnavailableAssessments(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	command := &cobra.Command{Use: "review", RunE: gitReviewCmd.RunE}
	var stdout, stderr bytes.Buffer
	if exit := executeCommand(context.Background(), command, nil, strings.NewReader(""), &stdout, &stderr); exit != 1 {
		t.Fatalf("exit = %d, want 1 for unavailable review; stderr = %q", exit, stderr.String())
	}
	var report review.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("invalid review output %q: %v", stdout.String(), err)
	}
	if report.Ready || report.GitAvailable || len(report.Assessments) == 0 {
		t.Fatalf("unexpected review report: %+v", report)
	}
}

func TestGitSmartCommitHonorsCancellationBeforeAndDuringCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git fixture requires a POSIX shell")
	}
	for _, phase := range []string{"preview", "commit"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			bin := t.TempDir()
			script := `#!/bin/sh
if [ "$3" = diff ]; then
  if [ "$5" = --name-only ]; then
    printf 'feature.go\000'
  elif [ "$RSCH_TEST_GIT_PHASE" = preview ]; then
    : > "$2/ready"
    sleep 5
  fi
elif [ "$3" = commit ]; then
  if [ "$RSCH_TEST_GIT_PHASE" = commit ]; then
    : > "$2/ready"
    sleep 5
  fi
  : > "$2/committed"
fi
`
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RSCH_TEST_GIT_PHASE", phase)
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
						if _, err := os.Stat(filepath.Join(root, "ready")); err == nil {
							cancel()
							ready <- true
							return
						}
					}
				}
			}()
			command := &cobra.Command{Use: "smart-commit", RunE: gitSmartCommitCmd.RunE}
			command.Flags().Bool("commit", true, "")
			command.Flags().Bool("json", false, "")
			command.Flags().String("message", "", "")
			command.SetContext(ctx)
			err := command.RunE(command, nil)
			cancel()
			if !<-ready {
				t.Fatal("Git fixture did not reach the cancellation point")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled Git command returned %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "committed")); !os.IsNotExist(err) {
				t.Fatalf("commit completed after cancellation: %v", err)
			}
		})
	}
}
