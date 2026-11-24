//go:build !windows

package performance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/opencode-ai/opencode/internal/process"
)

func inspectDisk(root string) DiskReport {
	path := normalizeResourcePath(root)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return DiskReport{Path: path, Reason: err.Error()}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(absolute, &stat); err != nil {
		return DiskReport{Path: absolute, Reason: err.Error()}
	}
	return DiskReport{Observed: true, Path: absolute, FreeBytes: uint64(stat.Bavail) * uint64(stat.Bsize), TotalBytes: uint64(stat.Blocks) * uint64(stat.Bsize)}
}

func platformProcessCount() (int, bool, string) {
	if runtime.GOOS != "linux" {
		result, err := process.RunOutput(context.Background(), process.Spec{Argv: []string{"ps", "-A", "-o", "pid="}})
		if err != nil {
			return 0, false, "process enumeration is unavailable on this platform"
		}
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(string(result.Output)), "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
		return count, true, "portable ps process count"
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false, "process enumeration is unavailable on this platform"
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := strconv.Atoi(entry.Name()); err == nil {
				count++
			}
		}
	}
	return count, true, "Linux procfs process count"
}
