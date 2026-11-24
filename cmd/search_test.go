package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestContextRetrievalHonorsCancellationAfterIndexBuild(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("repository architecture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cachePath := filepath.Join(root, ".rsch", "intelligence", "index.json")
	// Cancel at the transition from indexing to retrieval. The cache is only
	// written once indexing finishes, so this avoids timing-dependent sleeps.
	command := &cobra.Command{Use: "context", RunE: contextCmd.RunE}
	command.SetContext(&cancelAfterIndexContext{Context: ctx, cancel: cancel, cachePath: cachePath})
	command.Flags().Int("budget", 4000, "")
	command.Flags().Int("limit", 20, "")
	command.Flags().Bool("json", true, "")
	if err := command.RunE(command, []string{"repository"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("retrieval after cancellation returned %v", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("test must reach retrieval after successful indexing: %v", err)
	}
}

type cancelAfterIndexContext struct {
	context.Context
	cancel    context.CancelFunc
	cachePath string
}

func (c *cancelAfterIndexContext) Err() error {
	if _, err := os.Stat(c.cachePath); err == nil {
		c.cancel()
	}
	return c.Context.Err()
}
