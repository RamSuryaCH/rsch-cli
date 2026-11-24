package security

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strings"
)

type ScanOptions struct {
	MaxFiles           int
	MaxFileBytes       int64
	ExcludeDirectories []string
}

// ScanCoverage describes coverage within the explicitly excluded directory
// scope. LimitReached means traversal stopped and remaining files were not
// enumerated; the other counters describe only encountered paths.
type ScanCoverage struct {
	Complete            bool     `json:"complete"`
	FilesScanned        int      `json:"filesScanned"`
	UnreadablePaths     int      `json:"unreadablePaths"`
	OversizedFiles      int      `json:"oversizedFiles"`
	NonRegularFiles     int      `json:"nonRegularFiles"`
	LimitReached        bool     `json:"limitReached"`
	ExcludedDirectories []string `json:"excludedDirectories,omitempty"`
}

// WorkspaceScan retains counts rather than secret-bearing source evidence.
type WorkspaceScan struct {
	Findings       int          `json:"findings"`
	HighOrCritical int          `json:"highOrCritical"`
	Coverage       ScanCoverage `json:"coverage"`
	Reason         string       `json:"reason,omitempty"`
}

// ScanWorkspace performs a bounded scan of regular files confined to root.
// Missing coverage never establishes that the workspace passed security checks.
func ScanWorkspace(ctx context.Context, root string, options ScanOptions) WorkspaceScan {
	result := WorkspaceScan{Coverage: ScanCoverage{ExcludedDirectories: append([]string(nil), options.ExcludeDirectories...)}}
	if options.MaxFiles <= 0 || options.MaxFileBytes <= 0 || options.MaxFileBytes == math.MaxInt64 {
		result.Reason = "security scan requires positive file and byte limits within accounting capacity"
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Reason = "security scan incomplete: " + err.Error()
		return result
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		result.Coverage.UnreadablePaths++
		result.Reason = "security scan incomplete: " + err.Error()
		return result
	}
	defer workspace.Close()
	excluded := make(map[string]bool, len(options.ExcludeDirectories))
	for _, name := range options.ExcludeDirectories {
		excluded[name] = true
	}
	examined := 0
	var firstError error
	err = fs.WalkDir(workspace.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			result.Coverage.UnreadablePaths++
			if firstError == nil {
				firstError = walkErr
			}
			return nil
		}
		if entry.IsDir() {
			if name != "." && excluded[path.Base(name)] {
				return fs.SkipDir
			}
			return nil
		}
		if examined >= options.MaxFiles {
			result.Coverage.LimitReached = true
			return fs.SkipAll
		}
		examined++
		info, err := entry.Info()
		if err == nil && !info.Mode().IsRegular() {
			result.Coverage.NonRegularFiles++
			return nil
		}
		if err == nil && info.Size() > options.MaxFileBytes {
			result.Coverage.OversizedFiles++
			return nil
		}
		if err != nil {
			result.Coverage.UnreadablePaths++
			if firstError == nil {
				firstError = err
			}
			return nil
		}
		file, err := workspace.Open(name)
		if err != nil {
			result.Coverage.UnreadablePaths++
			if firstError == nil {
				firstError = err
			}
			return nil
		}
		defer file.Close()
		info, err = file.Stat()
		if err == nil && !info.Mode().IsRegular() {
			result.Coverage.NonRegularFiles++
			return nil
		}
		if err == nil && info.Size() > options.MaxFileBytes {
			result.Coverage.OversizedFiles++
			return nil
		}
		if err != nil {
			result.Coverage.UnreadablePaths++
			if firstError == nil {
				firstError = err
			}
			return nil
		}
		reader := &io.LimitedReader{R: scanContextReader{ctx: ctx, reader: file}, N: options.MaxFileBytes + 1}
		findings, scanErr := scanReader(name, reader)
		result.Findings += len(findings)
		for _, finding := range findings {
			if finding.Severity == High || finding.Severity == Critical {
				result.HighOrCritical++
			}
		}
		if reader.N == 0 {
			result.Coverage.OversizedFiles++
			return nil
		}
		if scanErr != nil {
			result.Coverage.UnreadablePaths++
			if firstError == nil {
				firstError = scanErr
			}
			return nil
		}
		result.Coverage.FilesScanned++
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	problems := []string{}
	if err != nil {
		problems = append(problems, err.Error())
	}
	if result.Coverage.UnreadablePaths > 0 {
		problem := fmt.Sprintf("%d unreadable path(s)", result.Coverage.UnreadablePaths)
		if firstError != nil {
			problem += ": " + firstError.Error()
		}
		problems = append(problems, problem)
	}
	if result.Coverage.OversizedFiles > 0 {
		problems = append(problems, fmt.Sprintf("%d file(s) exceed the %d-byte limit", result.Coverage.OversizedFiles, options.MaxFileBytes))
	}
	if result.Coverage.NonRegularFiles > 0 {
		problems = append(problems, fmt.Sprintf("%d nonregular file(s) were not scanned", result.Coverage.NonRegularFiles))
	}
	if result.Coverage.LimitReached {
		problems = append(problems, fmt.Sprintf("file limit %d reached; remaining paths were not enumerated", options.MaxFiles))
	}
	result.Coverage.Complete = len(problems) == 0
	if !result.Coverage.Complete {
		result.Reason = "security scan incomplete: " + strings.Join(problems, "; ")
	}
	return result
}

type scanContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r scanContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
