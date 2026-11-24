package interview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInterviewUsesRepositoryEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "migrations", "001.sql"), []byte("select 1"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := Start(context.Background(), root, "add auth")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Questions) != 3 {
		t.Fatalf("questions=%+v", session.Questions)
	}
	if err := session.Answer("migration-policy", "Backward-compatible"); err != nil {
		t.Fatal(err)
	}
	if len(session.Requirements()) != 1 {
		t.Fatal("expected answered requirement")
	}
}

func TestStartHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session, err := Start(ctx, root, "add auth")
	if !errors.Is(err, context.Canceled) || session.Goal != "" {
		t.Fatalf("canceled interview returned a session: %+v, %v", session, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch")); !os.IsNotExist(err) {
		t.Fatalf("canceled interview created repository state: %v", err)
	}
}

func TestRequirementsIgnoreBlankAnswersInLoadedSession(t *testing.T) {
	session := Session{Questions: []Question{
		{ID: "unanswered", Prompt: "Unused?", Answer: " \n\t"},
		{ID: "answered", Prompt: "Policy?", Answer: " Backward-compatible \n"},
	}}
	requirements := session.Requirements()
	if len(requirements) != 1 || requirements[0].ID != "answered" || requirements[0].Statement != "Policy? Answer: Backward-compatible" || !requirements[0].Confirmed {
		t.Fatalf("invalid confirmed requirements: %+v", requirements)
	}
}
