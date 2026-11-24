package explain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunExplainsFileWithEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "auth.go"), []byte("// validates auth tokens\npackage pkg\nimport \"crypto/sha256\"\nfunc Validate() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "auth_test.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), root, "pkg/auth.go")
	if err != nil {
		t.Fatal(err)
	}
	if report.Purpose == "" || len(report.Dependencies) != 1 || len(report.Tests) != 1 || len(report.Risks) == 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, target := range []string{".", "source.go"} {
		if report, err := Run(ctx, root, target); !errors.Is(err, context.Canceled) || report.Target != "" {
			t.Fatalf("canceled explanation returned a report: %+v, %v", report, err)
		}
	}
}

func TestEvidenceDiscoveryPropagatesCancellationAndWalkErrors(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, discover := range []func(context.Context, string) error{
		func(ctx context.Context, root string) error {
			_, err := findTests(ctx, root, "source.go")
			return err
		},
		func(ctx context.Context, root string) error {
			_, err := findCallers(ctx, root, "source.go", "func Example() {}")
			return err
		},
	} {
		if err := discover(ctx, root); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled directory scan error = %v", err)
		}
		if err := discover(context.Background(), filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing directory scan error = %v", err)
		}
	}
}

func TestRunRejectsOutsideTarget(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(context.Background(), root, "../outside"); err == nil {
		t.Fatal("expected outside target rejection")
	}
}

func TestRunRejectsSymlinkEscapes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "source.go"), []byte("// outside source\npackage outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, destination, target string }{
		{"file", filepath.Join(outside, "source.go"), "file"},
		{"directory", outside, "directory/source.go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Symlink(test.destination, filepath.Join(root, test.name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := Run(context.Background(), root, test.target); err == nil {
				t.Fatal("expected symlink escape rejection")
			}
		})
	}
}

func TestRunDoesNotReadSymlinkedEvidenceOutsideWorkspace(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package example\nfunc Example() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, contents string }{
		{"README.md", "# outside readme\n"},
		{"caller.go", "package example\nfunc Call() { Example() }\n"},
		{"source_test.go", "package example\n"},
	} {
		path := filepath.Join(outside, test.name)
		if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(root, test.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	repository, err := Run(context.Background(), root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if repository.Purpose != "repository workspace" {
		t.Fatalf("outside README influenced report: %+v", repository)
	}
	report, err := Run(context.Background(), root, "source.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Callers) != 0 || len(report.Tests) != 0 {
		t.Fatalf("outside evidence influenced report: %+v", report)
	}
}

func TestRunAllowsSymlinkedWorkspaceAndInternalTarget(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("// inside source\npackage example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink("source.go", filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), alias, filepath.Join(alias, "link.go"))
	if err != nil || report.Purpose != "inside source" {
		t.Fatalf("safe symlink target was not explained: %+v, %v", report, err)
	}
}
