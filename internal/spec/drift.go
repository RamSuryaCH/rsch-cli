package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/security"
)

const traceabilityFile = "traceability.json"

// Fingerprint is the immutable repository fact captured for a traceable file.
// Paths are always relative to the repository root and use slash separators.
type Fingerprint struct {
	Path     string    `json:"path"`
	Hash     string    `json:"hash"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type Traceability struct {
	Version  int                    `json:"version"`
	Captured time.Time              `json:"capturedAt"`
	Files    map[string]Fingerprint `json:"files"`
}

type DriftReport struct {
	CapturedAt time.Time     `json:"capturedAt"`
	CheckedAt  time.Time     `json:"checkedAt"`
	Changed    []Fingerprint `json:"changed"`
	Missing    []Fingerprint `json:"missing"`
	Added      []Fingerprint `json:"added"`
}

func (r DriftReport) HasDrift() bool {
	return len(r.Changed) > 0 || len(r.Missing) > 0 || len(r.Added) > 0
}

// Capture records hashes for the supplied repository files. With no paths,
// Capture includes all regular files while excluding generated/local state.
func (m Manager) Capture(workspace, name string, paths []string) (Traceability, error) {
	if err := m.Validate(name); err != nil {
		return Traceability{}, err
	}
	files, err := collectFingerprints(workspace, paths)
	if err != nil {
		return Traceability{}, err
	}
	trace := Traceability{Version: 1, Captured: time.Now().UTC(), Files: files}
	path, _ := m.Path(name)
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		return Traceability{}, err
	}
	if err := os.WriteFile(filepath.Join(path, traceabilityFile), append(data, '\n'), 0o600); err != nil {
		return Traceability{}, err
	}
	return trace, nil
}

func (m Manager) LoadTraceability(name string) (Traceability, error) {
	if err := m.Validate(name); err != nil {
		return Traceability{}, err
	}
	path, _ := m.Path(name)
	data, err := os.ReadFile(filepath.Join(path, traceabilityFile))
	if err != nil {
		return Traceability{}, fmt.Errorf("traceability is not captured: %w", err)
	}
	var trace Traceability
	if err := json.Unmarshal(data, &trace); err != nil {
		return Traceability{}, fmt.Errorf("invalid traceability: %w", err)
	}
	if trace.Version != 1 || trace.Files == nil {
		return Traceability{}, errors.New("invalid traceability version or file map")
	}
	return trace, nil
}

func (m Manager) CheckDrift(workspace, name string) (DriftReport, error) {
	trace, err := m.LoadTraceability(name)
	if err != nil {
		return DriftReport{}, err
	}
	// Re-scan the workspace so files introduced after capture are reported as
	// additions, including when the original trace captured an explicit subset.
	current, err := collectFingerprints(workspace, nil)
	if err != nil {
		return DriftReport{}, err
	}
	// Explicit captures can include safe symlinks or files in directories that
	// the workspace scan skips. Recheck those paths before declaring them missing.
	var omitted []string
	for path := range trace.Files {
		if _, exists := current[path]; !exists {
			omitted = append(omitted, path)
		}
	}
	if len(omitted) > 0 {
		tracked, err := collectFingerprints(workspace, omitted)
		if err != nil {
			return DriftReport{}, err
		}
		for path, fingerprint := range tracked {
			current[path] = fingerprint
		}
	}
	report := DriftReport{CapturedAt: trace.Captured, CheckedAt: time.Now().UTC(), Changed: []Fingerprint{}, Missing: []Fingerprint{}, Added: []Fingerprint{}}
	for path, before := range trace.Files {
		after, ok := current[path]
		if !ok {
			report.Missing = append(report.Missing, before)
			continue
		}
		if before.Hash != after.Hash || before.Size != after.Size {
			report.Changed = append(report.Changed, after)
		}
	}
	for path, after := range current {
		if _, ok := trace.Files[path]; !ok {
			report.Added = append(report.Added, after)
		}
	}
	sortFingerprints(report.Changed)
	sortFingerprints(report.Missing)
	sortFingerprints(report.Added)
	return report, nil
}

func collectFingerprints(root string, paths []string) (map[string]Fingerprint, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		walkRoot, resolveErr := filepath.EvalSymlinks(root)
		if resolveErr != nil {
			return nil, resolveErr
		}
		paths = []string{}
		err = filepath.WalkDir(walkRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != walkRoot && ignoredDirectory(filepath.Base(path)) {
					return fs.SkipDir
				}
				return nil
			}
			if !entry.Type().IsRegular() || ignoredDirectory(filepath.Base(filepath.Dir(path))) {
				return nil
			}
			rel, relErr := filepath.Rel(walkRoot, path)
			if relErr != nil {
				return relErr
			}
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make(map[string]Fingerprint, len(paths))
	for _, raw := range paths {
		rel := filepath.ToSlash(filepath.Clean(raw))
		if filepath.IsAbs(raw) {
			absolute, absErr := filepath.Abs(raw)
			if absErr != nil {
				return nil, absErr
			}
			relPath, relErr := filepath.Rel(root, absolute)
			if relErr != nil {
				return nil, relErr
			}
			rel = filepath.ToSlash(filepath.Clean(relPath))
		}
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("traceability path escapes workspace: %q", raw)
		}
		full, err := security.ResolveWorkspacePath(root, filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("resolve traceability path %q: %w", raw, err)
		}
		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(data)
		out[rel] = Fingerprint{Path: rel, Hash: hex.EncodeToString(hash[:]), Size: info.Size(), Modified: info.ModTime().UTC()}
	}
	return out, nil
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".rsch", "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}
func keys(values map[string]Fingerprint) []string {
	out := make([]string, 0, len(values))
	for path := range values {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
func sortFingerprints(values []Fingerprint) {
	sort.Slice(values, func(i, j int) bool { return values[i].Path < values[j].Path })
}
