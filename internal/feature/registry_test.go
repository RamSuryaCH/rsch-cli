package feature

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryRejectsUnverifiedVerifiedFeature(t *testing.T) {
	err := Validate(Registry{Version: 1, Features: []Entry{{ID: "a", Name: "A", Status: Verified}}})
	if err == nil {
		t.Fatal("expected missing evidence error")
	}
}

func TestRegistryRoundTrip(t *testing.T) {
	root := t.TempDir()
	registry := Registry{Version: 1, Features: []Entry{{ID: "repo", Name: "Repository", Status: Verified, Verification: []string{"evidence.txt"}}}}
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, registry); err != nil {
		t.Fatal(err)
	}
	if mode := mustStat(t, Path(root)).Mode().Perm(); mode != 0o600 {
		t.Fatalf("permissions=%o", mode)
	}
	loaded, err := Load(root)
	if err != nil || len(loaded.Features) != 1 {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestRegistryEvidenceMustBeAFileInsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "evidence", "check.txt")
	if err := os.WriteFile(inside, []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{".", "evidence", "../outside.txt", inside, "missing.txt"} {
		t.Run(reference, func(t *testing.T) {
			registry := Registry{Version: 1, Features: []Entry{{ID: "a", Name: "A", Status: Verified, Verification: []string{reference}}}}
			if err := ValidateAt(root, registry); err == nil {
				t.Fatalf("accepted invalid evidence reference %q", reference)
			}
		})
	}
	registry := Registry{Version: 1, Features: []Entry{{ID: "a", Name: "A", Status: Verified, Verification: []string{"evidence/check.txt"}}}}
	if err := ValidateAt(root, registry); err != nil {
		t.Fatalf("valid local evidence: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "external-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	registry.Features[0].Verification = []string{"external-link"}
	if err := ValidateAt(root, registry); err == nil {
		t.Fatal("accepted symlink evidence outside workspace")
	}
	if err := os.Symlink(inside, filepath.Join(root, "internal-link")); err != nil {
		t.Fatal(err)
	}
	registry.Features[0].Verification = []string{"internal-link"}
	if err := ValidateAt(root, registry); err != nil {
		t.Fatalf("valid local symlink evidence: %v", err)
	}
	linkedRoot := filepath.Join(parent, "linked-workspace")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAt(linkedRoot, registry); err != nil {
		t.Fatalf("valid symlink workspace: %v", err)
	}
}
