// Package checkpoint provides recoverable workspace snapshots with explicit
// conflict plans and transactional restore.
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

const (
	manifestVersion  = 3
	fileStateVersion = 2
	legacyFileMode   = 0o600
)

type File struct {
	Path   string `json:"path"`
	Hash   string `json:"hash,omitempty"`
	Exists bool   `json:"exists"`
	Mode   uint32 `json:"mode,omitempty"`
}

type Metadata struct {
	SessionID    string   `json:"sessionId,omitempty"`
	GoalID       string   `json:"goalId,omitempty"`
	TaskID       string   `json:"taskId,omitempty"`
	AgentID      string   `json:"agentId,omitempty"`
	Verification []string `json:"verification,omitempty"`
}

type Manifest struct {
	Version   int       `json:"version,omitempty"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Files     []File    `json:"files"`
	Metadata  Metadata  `json:"metadata,omitempty"`
}

type RestorePlan struct {
	Manifest  Manifest `json:"manifest"`
	Affected  []string `json:"affected"`
	Conflicts []string `json:"conflicts,omitempty"`
	GitStatus []string `json:"gitStatus,omitempty"`
	Risk      string   `json:"risk"`
	Approved  bool     `json:"approved"`
}

type Manager struct{ Root string }

type fileState struct {
	Path   string
	Data   []byte
	Exists bool
	Mode   os.FileMode
}

type restoreChange struct {
	Before fileState
	After  fileState
}

func NewManager(workspace string) Manager {
	return Manager{Root: filepath.Join(workspace, ".rsch", "checkpoints")}
}

func (m Manager) Create(id string, paths []string) (Manifest, error) {
	return m.CreateWithMetadata(id, paths, Metadata{})
}

func (m Manager) CreateWithMetadata(id string, paths []string, metadata Metadata) (Manifest, error) {
	if !safeID(id) {
		return Manifest{}, errors.New("invalid checkpoint id")
	}
	if len(paths) == 0 {
		return Manifest{}, errors.New("at least one path is required")
	}
	workspace, err := m.workspace()
	if err != nil {
		return Manifest{}, err
	}
	root, err := m.storageRoot(workspace)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Manifest{}, err
	}
	finalDir := filepath.Join(root, id)
	if _, err := os.Lstat(finalDir); err == nil {
		return Manifest{}, fmt.Errorf("checkpoint %q already exists", id)
	} else if !os.IsNotExist(err) {
		return Manifest{}, err
	}
	tempDir, err := os.MkdirTemp(root, ".checkpoint-"+id+"-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tempDir)

	manifest := Manifest{Version: manifestVersion, ID: id, CreatedAt: time.Now().UTC(), Metadata: metadata}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		state, relative, stateErr := readWorkspaceState(workspace, path)
		if stateErr != nil {
			return Manifest{}, stateErr
		}
		if seen[relative] {
			return Manifest{}, fmt.Errorf("duplicate checkpoint path %q", relative)
		}
		seen[relative] = true
		entry := File{Path: relative, Exists: state.Exists}
		if state.Exists {
			sum := sha256.Sum256(state.Data)
			entry.Hash = hex.EncodeToString(sum[:])
			entry.Mode = uint32(state.Mode.Perm())
			target, joinErr := safeJoin(checkpointDataDir(tempDir, manifest), relative)
			if joinErr != nil {
				return Manifest{}, joinErr
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return Manifest{}, err
			}
			if err := os.WriteFile(target, state.Data, 0o600); err != nil {
				return Manifest{}, err
			}
		}
		manifest.Files = append(manifest.Files, entry)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(tempDir, "manifest.json"), append(encoded, '\n'), 0o600); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Restore is the conservative API: it refuses to overwrite any path that no
// longer matches the checkpoint. Callers that displayed Plan and received
// explicit approval should use RestoreWithApproval.
func (m Manager) Restore(id string) error {
	plan, err := m.Plan(id)
	if err != nil {
		return err
	}
	if len(plan.Conflicts) > 0 {
		return fmt.Errorf("restore conflict: %s changed since checkpoint", strings.Join(plan.Conflicts, ", "))
	}
	return m.applyPlan(plan)
}

func (m Manager) Plan(id string) (RestorePlan, error) {
	manifest, err := m.loadManifest(id)
	if err != nil {
		return RestorePlan{}, err
	}
	workspace, err := m.workspace()
	if err != nil {
		return RestorePlan{}, err
	}
	plan := RestorePlan{Manifest: manifest, Affected: []string{}, GitStatus: []string{}, Risk: "low"}
	seen := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if seen[file.Path] {
			return RestorePlan{}, fmt.Errorf("checkpoint contains duplicate path %q", file.Path)
		}
		seen[file.Path] = true
		target, targetErr := workspaceTarget(workspace, file.Path)
		if targetErr != nil {
			return RestorePlan{}, targetErr
		}
		current, stateErr := readFileState(target)
		if stateErr != nil {
			return RestorePlan{}, stateErr
		}
		plan.Affected = append(plan.Affected, file.Path)
		if !matchesManifest(current, manifest, file) {
			plan.Conflicts = append(plan.Conflicts, file.Path)
		}
	}
	sort.Strings(plan.Affected)
	sort.Strings(plan.Conflicts)
	if result, gitErr := process.RunOutput(context.Background(), process.Spec{Argv: []string{"git", "-C", workspace, "status", "--porcelain=v1", "--untracked-files=all"}}); gitErr == nil && strings.TrimSpace(string(result.Output)) != "" {
		plan.GitStatus = strings.Split(strings.TrimSpace(string(result.Output)), "\n")
	}
	if len(plan.Conflicts) > 0 || len(plan.GitStatus) > 0 {
		plan.Risk = "high"
	}
	return plan, nil
}

func (m Manager) RestoreWithApproval(id string, approved bool) error {
	plan, err := m.Plan(id)
	if err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("restore approval required; affected files=%d conflicts=%d risk=%s", len(plan.Affected), len(plan.Conflicts), plan.Risk)
	}
	plan.Approved = true
	return m.applyPlan(plan)
}

func (m Manager) applyPlan(plan RestorePlan) error {
	workspace, err := m.workspace()
	if err != nil {
		return err
	}
	dir, err := m.checkpointDir(plan.Manifest.ID)
	if err != nil {
		return err
	}
	changes := make([]restoreChange, 0, len(plan.Manifest.Files))
	for _, file := range plan.Manifest.Files {
		target, targetErr := workspaceTarget(workspace, file.Path)
		if targetErr != nil {
			return targetErr
		}
		before, stateErr := readFileState(target)
		if stateErr != nil {
			return stateErr
		}
		after := fileState{Path: target, Exists: fileExists(plan.Manifest, file), Mode: fileMode(plan.Manifest, file)}
		if after.Exists {
			source, joinErr := safeJoin(checkpointDataDir(dir, plan.Manifest), file.Path)
			if joinErr != nil {
				return joinErr
			}
			resolvedSource, resolveErr := security.ResolveWorkspacePath(dir, source)
			if resolveErr != nil {
				return fmt.Errorf("invalid checkpoint data for %s: %w", file.Path, resolveErr)
			}
			sourceInfo, statErr := os.Lstat(source)
			if statErr != nil {
				return statErr
			}
			if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
				return fmt.Errorf("checkpoint data for %s is not a regular file", file.Path)
			}
			data, readErr := os.ReadFile(resolvedSource)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != file.Hash {
				return fmt.Errorf("checkpoint data for %s failed checksum validation", file.Path)
			}
			after.Data = data
		}
		changes = append(changes, restoreChange{Before: before, After: after})
	}
	return applyRestoreTransaction(changes, applyFileState)
}

func applyRestoreTransaction(changes []restoreChange, apply func(fileState) error) error {
	if apply == nil {
		return errors.New("restore state applier is required")
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].After.Path < changes[j].After.Path })
	for index := 1; index < len(changes); index++ {
		if changes[index-1].After.Path == changes[index].After.Path {
			return fmt.Errorf("duplicate restore target %q", changes[index].After.Path)
		}
	}
	applied := make([]restoreChange, 0, len(changes))
	for _, change := range changes {
		applied = append(applied, change)
		if err := apply(change.After); err != nil {
			rollbackFailures := make([]string, 0)
			for index := len(applied) - 1; index >= 0; index-- {
				if rollbackErr := apply(applied[index].Before); rollbackErr != nil {
					rollbackFailures = append(rollbackFailures, applied[index].Before.Path+": "+rollbackErr.Error())
				}
			}
			if len(rollbackFailures) > 0 {
				return fmt.Errorf("restore checkpoint: %w; rollback failed: %s", err, strings.Join(rollbackFailures, "; "))
			}
			return fmt.Errorf("restore checkpoint: %w; all affected paths were rolled back", err)
		}
	}
	return nil
}

func applyFileState(state fileState) error {
	if !state.Exists {
		info, err := os.Lstat(state.Path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return fmt.Errorf("restore target is a directory: %s", state.Path)
		}
		return os.Remove(state.Path)
	}
	if err := os.MkdirAll(filepath.Dir(state.Path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(state.Path), ".rsch-restore-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(state.Mode.Perm()); err != nil {
		cleanup()
		return err
	}
	if _, err := temp.Write(state.Data); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := replaceFile(tempPath, state.Path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func (m Manager) List() ([]Manifest, error) {
	workspace, err := m.workspace()
	if err != nil {
		return nil, err
	}
	root, err := m.storageRoot(workspace)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	manifests := []Manifest{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".checkpoint-") {
			continue
		}
		manifest, err := m.loadManifest(entry.Name())
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].CreatedAt.Before(manifests[j].CreatedAt) })
	return manifests, nil
}

func (m Manager) loadManifest(id string) (Manifest, error) {
	dir, err := m.checkpointDir(id)
	if err != nil {
		return Manifest{}, err
	}
	path, err := security.ResolveWorkspacePath(dir, filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("invalid checkpoint manifest path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.ID != id {
		return Manifest{}, fmt.Errorf("checkpoint manifest ID %q does not match %q", manifest.ID, id)
	}
	if manifest.Version > manifestVersion {
		return Manifest{}, fmt.Errorf("unsupported checkpoint manifest version %d", manifest.Version)
	}
	return manifest, nil
}

func (m Manager) workspace() (string, error) {
	workspace, err := filepath.Abs(filepath.Dir(filepath.Dir(m.Root)))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(workspace)
}

func (m Manager) storageRoot(workspace string) (string, error) {
	root, err := filepath.Abs(m.Root)
	if err != nil {
		return "", err
	}
	root, err = security.ResolveWorkspacePath(workspace, root)
	if err != nil {
		return "", fmt.Errorf("invalid checkpoint storage path: %w", err)
	}
	return root, nil
}

func (m Manager) checkpointDir(id string) (string, error) {
	if !safeID(id) {
		return "", errors.New("invalid checkpoint id")
	}
	workspace, err := m.workspace()
	if err != nil {
		return "", err
	}
	root, err := m.storageRoot(workspace)
	if err != nil {
		return "", err
	}
	dir, err := security.ResolveWorkspacePath(root, filepath.Join(root, id))
	if err != nil {
		return "", fmt.Errorf("invalid checkpoint directory: %w", err)
	}
	return dir, nil
}

func readWorkspaceState(workspace, path string) (fileState, string, error) {
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(workspace, target)
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fileState{}, "", fmt.Errorf("checkpoint paths cannot be symbolic links: %s", path)
	}
	resolved, err := security.ResolveWorkspacePath(workspace, target)
	if err != nil {
		return fileState{}, "", fmt.Errorf("path is outside workspace: %w", err)
	}
	relative, err := filepath.Rel(workspace, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fileState{}, "", errors.New("path is outside workspace")
	}
	state, err := readFileState(resolved)
	return state, filepath.ToSlash(relative), err
}

func readFileState(path string) (fileState, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return fileState{Path: path}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fileState{}, fmt.Errorf("checkpoint paths cannot be symbolic links: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fileState{}, fmt.Errorf("checkpoint path is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{Path: path, Data: data, Exists: true, Mode: info.Mode().Perm()}, nil
}

func workspaceTarget(workspace, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid checkpoint path %q", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid checkpoint path %q", relative)
	}
	target := filepath.Join(workspace, clean)
	resolved, err := security.ResolveWorkspacePath(workspace, target)
	if err != nil {
		return "", fmt.Errorf("invalid checkpoint path %q: %w", relative, err)
	}
	if resolved != target {
		return "", fmt.Errorf("checkpoint restore path cannot contain symbolic links: %s", relative)
	}
	return resolved, nil
}

func safeJoin(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid checkpoint path %q", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid checkpoint path %q", relative)
	}
	return filepath.Join(root, clean), nil
}

// Version 3 separates file contents from metadata so a workspace manifest.json
// cannot overwrite the checkpoint's own manifest. Older layouts remain readable.
func checkpointDataDir(dir string, manifest Manifest) string {
	if manifest.Version >= 3 {
		return filepath.Join(dir, "files")
	}
	return dir
}

func matchesManifest(current fileState, manifest Manifest, file File) bool {
	expectedExists := fileExists(manifest, file)
	if current.Exists != expectedExists {
		return false
	}
	if !expectedExists {
		return true
	}
	sum := sha256.Sum256(current.Data)
	if hex.EncodeToString(sum[:]) != file.Hash {
		return false
	}
	return manifest.Version < fileStateVersion || current.Mode.Perm() == fileMode(manifest, file).Perm()
}

func fileExists(manifest Manifest, file File) bool {
	if manifest.Version < fileStateVersion {
		return true
	}
	return file.Exists
}

func fileMode(manifest Manifest, file File) os.FileMode {
	if manifest.Version < fileStateVersion {
		return legacyFileMode
	}
	return os.FileMode(file.Mode).Perm()
}

func safeID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/\\") && id != "." && id != ".."
}
