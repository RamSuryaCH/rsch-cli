package gitsafety

import (
	"context"
	"testing"
)

func TestOverlapDetectsSharedFiles(t *testing.T) {
	overlap := Overlap([]string{"a.go", "b.go"}, []string{"b.go", "c.go"})
	if len(overlap) != 1 || overlap[0] != "b.go" {
		t.Fatalf("unexpected overlap: %v", overlap)
	}
}

func TestInspectDoesNotFabricateGitEvidence(t *testing.T) {
	status, err := Inspect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if status.Available {
		t.Fatalf("expected unavailable Git capability: %+v", status)
	}
	if status.Reason == "" {
		t.Fatal("expected explicit unavailability reason")
	}
}
