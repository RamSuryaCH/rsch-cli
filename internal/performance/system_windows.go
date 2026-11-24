//go:build windows

package performance

import (
	"context"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/opencode-ai/opencode/internal/process"
)

func inspectDisk(root string) DiskReport {
	path := normalizeResourcePath(root)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return DiskReport{Path: path, Reason: err.Error()}
	}
	nativePath, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return DiskReport{Path: absolute, Reason: err.Error()}
	}
	var free, total, _ uint64
	if err := windows.GetDiskFreeSpaceEx(nativePath, &free, &total, nil); err != nil {
		return DiskReport{Path: absolute, Reason: err.Error()}
	}
	return DiskReport{Observed: true, Path: absolute, FreeBytes: free, TotalBytes: total}
}

func platformProcessCount() (int, bool, string) {
	result, err := process.RunOutput(context.Background(), process.Spec{Argv: []string{"tasklist", "/FO", "CSV", "/NH"}})
	if err != nil {
		return 0, false, "Windows tasklist process enumeration is unavailable: " + err.Error()
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(result.Output)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, true, "Windows tasklist process count"
}
