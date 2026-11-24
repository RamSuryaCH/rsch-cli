package logging

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionLogsUsePrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are platform-specific")
	}
	root := t.TempDir()
	previous := MessageDir
	MessageDir = root
	t.Cleanup(func() { MessageDir = previous })

	path := AppendToSessionLogFile("12345678-session", "event.json", "safe")
	if path == "" {
		t.Fatal("session log was not written")
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("session directory permissions = %o, want 700", got)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("session log permissions = %o, want 600", got)
	}
}

func TestSessionLogsHandleShortIDsAndRejectPathTraversal(t *testing.T) {
	root := t.TempDir()
	previous := MessageDir
	MessageDir = root
	t.Cleanup(func() { MessageDir = previous })

	if got := GetSessionPrefix(""); got != "" {
		t.Fatalf("empty session prefix = %q", got)
	}
	if got := AppendToSessionLogFile("abc", "event.json", "safe"); got != filepath.Join(root, "abc", "event.json") {
		t.Fatalf("short session log path = %q", got)
	}
	for _, tc := range []struct{ session, filename string }{
		{"../escape", "event.json"},
		{"..\\escape", "event.json"},
		{"12345678", "../../escape"},
		{"12345678", "..\\escape"},
		{"12345678", "/absolute"},
		{"12345678", "stream:secret"},
		{".", "event.json"},
		{"12345678", ".."},
	} {
		if got := AppendToSessionLogFile(tc.session, tc.filename, "unsafe"); got != "" {
			t.Errorf("accepted session %q and filename %q: %s", tc.session, tc.filename, got)
		}
	}
}

func TestSessionLogsRejectSymlinkDestinations(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	previous := MessageDir
	MessageDir = root
	t.Cleanup(func() { MessageDir = previous })
	if err := os.Symlink(outside, filepath.Join(root, "12345678")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := AppendToSessionLogFile("12345678-session", "event.json", "unsafe"); got != "" {
		t.Fatalf("accepted symlinked session directory: %q", got)
	}
	if err := os.Mkdir(filepath.Join(root, "abcdefgh"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "event.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "abcdefgh", "event.json")); err != nil {
		t.Fatal(err)
	}
	if got := AppendToSessionLogFile("abcdefgh-session", "event.json", "unsafe"); got != "" {
		t.Fatalf("accepted symlinked session log: %q", got)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatalf("external destination changed: %q, %v", data, err)
	}
}

func TestRecoverPanicRedactsCredentialsAndPreservesExistingLogs(t *testing.T) {
	t.Chdir(t.TempDir())
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cleanupCount := 0
	for range 2 {
		func() {
			defer RecoverPanic("../worker", func() { cleanupCount++ })
			panic(`password="test-panic-sensitive-value"`)
		}()
	}
	if cleanupCount != 2 {
		t.Fatalf("cleanup called %d times, want 2", cleanupCount)
	}
	paths, err := filepath.Glob("rsch-panic-*.log")
	if err != nil || len(paths) != 2 {
		t.Fatalf("expected two distinct panic logs: %v, %v", paths, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "test-panic-sensitive-value") || !strings.Contains(string(data), "[REDACTED]") {
			t.Fatalf("panic log contains unredacted credentials: %s", data)
		}
		if !strings.Contains(string(data), "Stack Trace:") {
			t.Fatal("panic log lost diagnostic stack trace")
		}
	}
	if strings.Contains(logs.String(), "test-panic-sensitive-value") {
		t.Fatal("panic credentials reached the default logger")
	}
}
