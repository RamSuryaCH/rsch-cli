//go:build !windows

package prompt

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadContextFileRejectsNamedPipes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "context.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep both ends open and provide more than the read limit so an unsafe
	// implementation fails by consuming pipe data rather than hanging the test.
	pipe, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Close() })
	if _, err := pipe.Write([]byte("pipe data")); err != nil {
		t.Fatal(err)
	}
	content, truncated, ok := readContextFile(path, 4)
	if ok || truncated || content != "" {
		t.Fatalf("named pipe accepted as repository context: content=%q truncated=%v ok=%v", content, truncated, ok)
	}
}
