package performance

import (
	"runtime"
	"strings"
)

// SystemSnapshot contains bounded, read-only process and host resource
// observations. Each dimension carries its own Observed flag so diagnostics
// never imply telemetry that the current platform does not provide.
type SystemSnapshot struct {
	CPU       CPUReport     `json:"cpu"`
	Memory    MemoryReport  `json:"memory"`
	Disk      DiskReport    `json:"disk"`
	Processes ProcessReport `json:"processes"`
	Network   NetworkReport `json:"network"`
}

type CPUReport struct {
	Observed            bool    `json:"observed"`
	Logical             int     `json:"logical,omitempty"`
	GOMAXPROCS          int     `json:"gomaxprocs,omitempty"`
	UtilizationObserved bool    `json:"utilizationObserved"`
	UtilizationPercent  float64 `json:"utilizationPercent,omitempty"`
	Reason              string  `json:"reason,omitempty"`
}

type MemoryReport struct {
	Observed bool   `json:"observed"`
	Alloc    uint64 `json:"alloc,omitempty"`
	Sys      uint64 `json:"sys,omitempty"`
	Heap     uint64 `json:"heap,omitempty"`
}

type DiskReport struct {
	Observed   bool   `json:"observed"`
	Path       string `json:"path,omitempty"`
	FreeBytes  uint64 `json:"freeBytes,omitempty"`
	TotalBytes uint64 `json:"totalBytes,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ProcessReport struct {
	Observed bool   `json:"observed"`
	Count    int    `json:"count,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type NetworkReport struct {
	Observed bool   `json:"observed"`
	Reason   string `json:"reason,omitempty"`
}

func InspectSystem(root string) SystemSnapshot {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return SystemSnapshot{
		CPU:       CPUReport{Observed: true, Logical: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Reason: "portable runtime reports CPU capacity; utilization telemetry is unavailable"},
		Memory:    MemoryReport{Observed: true, Alloc: stats.Alloc, Sys: stats.Sys, Heap: stats.HeapAlloc},
		Disk:      inspectDisk(root),
		Processes: inspectProcesses(),
		Network:   NetworkReport{Observed: false, Reason: "network utilization telemetry is not collected by the portable runtime"},
	}
}

func inspectProcesses() ProcessReport {
	count, observed, reason := platformProcessCount()
	return ProcessReport{Observed: observed, Count: count, Reason: reason}
}

func normalizeResourcePath(root string) string {
	if strings.TrimSpace(root) == "" {
		return "."
	}
	return root
}
