package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTraceabilityDetectsChangedMissingAndAddedFiles(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "removed.txt"), []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Capture(root, "feature", []string{"tracked.txt", "removed.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "added.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := manager.CheckDrift(root, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasDrift() || len(report.Changed) != 1 || len(report.Missing) != 1 || len(report.Added) != 1 {
		t.Fatalf("unexpected drift report: %+v", report)
	}
}

func TestTraceabilityRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Capture(root, "feature", []string{"../outside"}); err == nil {
		t.Fatal("expected escaping path error")
	}
}

func TestTraceabilityRejectsSymlinkEscapesWithoutReplacingCapture(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	manager := NewManager(root)
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "external.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Capture(root, "feature", []string{"tracked.txt"}); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"external.txt", filepath.Join(outside, "external.txt")},
		{"external-dir", outside},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	for _, path := range []string{"external.txt", "external-dir/external.txt", filepath.Join(root, "external.txt")} {
		t.Run(path, func(t *testing.T) {
			if _, err := manager.Capture(root, "feature", []string{path}); err == nil {
				t.Fatal("expected external symlink rejection")
			}
			trace, err := manager.LoadTraceability("feature")
			if err != nil || len(trace.Files) != 1 || trace.Files["tracked.txt"].Path != "tracked.txt" {
				t.Fatalf("rejected capture replaced evidence: %+v, %v", trace, err)
			}
		})
	}
}

func TestTraceabilityAllowsInternalSymlink(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	trace, err := manager.Capture(root, "feature", []string{"tracked.txt", "linked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Files) != 2 || trace.Files["tracked.txt"].Hash != trace.Files["linked.txt"].Hash {
		t.Fatalf("safe symlink did not capture its target: %+v", trace)
	}
	report, err := manager.CheckDrift(root, "feature")
	if err != nil || report.HasDrift() {
		t.Fatalf("unchanged internal symlink reported drift: %+v, %v", report, err)
	}
	if err := os.Remove(filepath.Join(root, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	report, err = manager.CheckDrift(root, "feature")
	if err != nil || len(report.Missing) != 2 {
		t.Fatalf("missing symlink target was not reported: %+v, %v", report, err)
	}
}

func TestTraceabilityRechecksExplicitlyTrackedIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dist", "output.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Capture(root, "feature", []string{"dist/output.txt"}); err != nil {
		t.Fatal(err)
	}
	report, err := manager.CheckDrift(root, "feature")
	if err != nil || report.HasDrift() {
		t.Fatalf("unchanged explicitly tracked file reported drift: %+v, %v", report, err)
	}
	if err := os.WriteFile(path, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = manager.CheckDrift(root, "feature")
	if err != nil || len(report.Changed) != 1 || len(report.Missing) != 0 {
		t.Fatalf("explicitly tracked change was not reported: %+v, %v", report, err)
	}
}

func TestTraceabilityScansSymlinkedWorkspace(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manager := NewManager(alias)
	if _, err := manager.New("feature"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	trace, err := manager.Capture(alias, "feature", nil)
	if err != nil || len(trace.Files) != 1 || trace.Files["tracked.txt"].Path != "tracked.txt" {
		t.Fatalf("symlinked workspace capture lost its files: %+v, %v", trace, err)
	}
	report, err := manager.CheckDrift(alias, "feature")
	if err != nil || report.HasDrift() {
		t.Fatalf("unchanged symlinked workspace reported drift: %+v, %v", report, err)
	}
}
