package gitcommit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
)

func TestPreviewStagedPreservesLiteralFilenames(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init")
	names := []string{"with spaces.txt", "日本語.txt"}
	if runtime.GOOS != "windows" {
		names = append(names, "tab\tname.txt", "line\nbreak.txt")
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte("file contents"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, root, "add", "--", name)
	}
	preview, err := PreviewStaged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(preview.Files, names) {
		t.Fatalf("preview files = %q, want %q", preview.Files, names)
	}
}

func TestPreviewStagedRejectsNestedAndQuotedSensitivePaths(t *testing.T) {
	names := []string{"config/.env", "config/.env.production", "日本語.pem"}
	if runtime.GOOS != "windows" {
		names = append(names, "private\tfile.pem", "private\nfile.key")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			gitRun(t, root, "init")
			target := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("test fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitRun(t, root, "add", "--", name)
			if _, err := PreviewStaged(context.Background(), root); err == nil {
				t.Fatalf("expected sensitive path %q to be refused", name)
			}
		})
	}
}

func TestPreviewStagedInfersMessageAndRejectsSecrets(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "feature.go"), []byte("package feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "feature.go")
	preview, err := PreviewStaged(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Message == "" || len(preview.Files) != 1 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".env")
	if _, err := PreviewStaged(context.Background(), root); err == nil {
		t.Fatal("expected secret refusal")
	}
}

func gitRun(t *testing.T, root string, args ...string) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
}
