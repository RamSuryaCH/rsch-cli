package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanWorkspaceReportsExactAndTruncatedCoverage(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceScanFile(t, root, "a.txt", "clean")
	writeWorkspaceScanFile(t, root, "b.txt", "clean")
	options := ScanOptions{MaxFiles: 2, MaxFileBytes: 100}
	result := ScanWorkspace(context.Background(), root, options)
	if !result.Coverage.Complete || result.Coverage.FilesScanned != 2 || result.Coverage.LimitReached {
		t.Fatalf("exact-limit coverage: %+v", result)
	}
	writeWorkspaceScanFile(t, root, "c.txt", "clean")
	result = ScanWorkspace(context.Background(), root, options)
	if result.Coverage.Complete || result.Coverage.FilesScanned != 2 || !result.Coverage.LimitReached || result.Reason == "" {
		t.Fatalf("truncated coverage: %+v", result)
	}
}

func TestScanWorkspaceReportsOversizedAndExcludedFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceScanFile(t, root, "fits.txt", "1234")
	writeWorkspaceScanFile(t, root, "large.txt", "12345")
	writeWorkspaceScanFile(t, root, "excluded/large.txt", "12345")
	result := ScanWorkspace(context.Background(), root, ScanOptions{MaxFiles: 10, MaxFileBytes: 4, ExcludeDirectories: []string{"excluded"}})
	if result.Coverage.Complete || result.Coverage.FilesScanned != 1 || result.Coverage.OversizedFiles != 1 || result.Reason == "" {
		t.Fatalf("oversized coverage: %+v", result)
	}
	if len(result.Coverage.ExcludedDirectories) != 1 || result.Coverage.ExcludedDirectories[0] != "excluded" {
		t.Fatalf("scan scope was not preserved: %+v", result.Coverage)
	}
}

func TestScanWorkspaceDoesNotFollowExternalSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeWorkspaceScanFile(t, outside, "secret.txt", "api_key"+" = \""+strings.Repeat("x", 20)+"\"")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "external.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result := ScanWorkspace(context.Background(), root, ScanOptions{MaxFiles: 10, MaxFileBytes: 100})
	if result.Coverage.Complete || result.Coverage.NonRegularFiles != 1 || result.Findings != 0 || result.Reason == "" {
		t.Fatalf("external symlink coverage: %+v", result)
	}
}

func TestScanWorkspacePreservesFindingsBeforeReadFailure(t *testing.T) {
	root := t.TempDir()
	content := "api_key" + " = \"" + strings.Repeat("x", 20) + "\"\n" + strings.Repeat("x", (2<<20)+1)
	writeWorkspaceScanFile(t, root, "partial.txt", content)
	result := ScanWorkspace(context.Background(), root, ScanOptions{MaxFiles: 10, MaxFileBytes: 4 << 20})
	if result.Coverage.Complete || result.Coverage.UnreadablePaths != 1 || result.HighOrCritical != 1 || result.Findings != 1 || result.Reason == "" {
		t.Fatalf("partial scan lost observed findings or claimed completeness: %+v", result)
	}
}

func TestScanWorkspaceCancellationAndInvalidLimits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := ScanWorkspace(ctx, filepath.Join(t.TempDir(), "missing"), ScanOptions{MaxFiles: 10, MaxFileBytes: 100})
	if result.Coverage.Complete || !strings.Contains(result.Reason, context.Canceled.Error()) || result.Coverage.UnreadablePaths != 0 {
		t.Fatalf("canceled scan: %+v", result)
	}
	result = ScanWorkspace(context.Background(), t.TempDir(), ScanOptions{})
	if result.Coverage.Complete || result.Reason == "" {
		t.Fatalf("unbounded scan accepted: %+v", result)
	}
}

func writeWorkspaceScanFile(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
