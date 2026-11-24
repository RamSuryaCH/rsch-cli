// Package worktree provides explicit Git worktree isolation for concurrent
// tasks. Creation refuses dirty repositories and paths outside the RSCH area.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencode-ai/opencode/internal/gitsafety"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

type Manager struct {
	Root string
	Max  int
}

func NewManager(workspace string) Manager {
	return Manager{Root: filepath.Join(workspace, ".rsch", "worktrees")}
}

func NewManagerWithLimit(workspace string, max int) Manager {
	return Manager{Root: filepath.Join(workspace, ".rsch", "worktrees"), Max: max}
}
func (m Manager) Path(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return "", errors.New("invalid worktree name")
	}
	return filepath.Join(m.Root, name), nil
}
func (m Manager) Create(ctx context.Context, workspace, name string) (string, error) {
	path, err := m.confinedPath(workspace, name)
	if err != nil {
		return "", err
	}
	if filepath.Clean(workspace) == filepath.Clean(path) {
		return "", errors.New("worktree cannot be the workspace")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", errors.New("worktree already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if m.Max > 0 {
		if entries, readErr := os.ReadDir(filepath.Dir(path)); readErr == nil {
			active := 0
			for _, entry := range entries {
				if entry.IsDir() {
					active++
				}
			}
			if active >= m.Max {
				return "", fmt.Errorf("worktree resource limit reached: active=%d limit=%d", active, m.Max)
			}
		} else if !os.IsNotExist(readErr) {
			return "", readErr
		}
	}
	if err := gitsafety.RequireClean(ctx, workspace); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "worktree", "add", "--detach", path, "HEAD"}})
	if err != nil {
		return "", fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(string(result.Output)))
	}
	return path, nil
}

// CreateFork creates a detached worktree and carries the parent's current
// tracked and untracked changes into it. The parent is never reset, staged, or
// otherwise modified. This is deliberately separate from Create, whose clean
// repository precondition remains useful for concurrent agent worktrees.
func (m Manager) CreateFork(ctx context.Context, workspace, name string) (string, error) {
	status, err := gitsafety.Inspect(ctx, workspace)
	if err != nil {
		return "", err
	}
	if status.Clean {
		return m.Create(ctx, workspace, name)
	}
	path, err := m.confinedPath(workspace, name)
	if err != nil {
		return "", err
	}
	if filepath.Clean(workspace) == filepath.Clean(path) {
		return "", errors.New("worktree cannot be the workspace")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", errors.New("worktree already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if m.Max > 0 {
		if entries, readErr := os.ReadDir(filepath.Dir(path)); readErr == nil {
			active := 0
			for _, entry := range entries {
				if entry.IsDir() {
					active++
				}
			}
			if active >= m.Max {
				return "", fmt.Errorf("worktree resource limit reached: active=%d limit=%d", active, m.Max)
			}
		} else if !os.IsNotExist(readErr) {
			return "", readErr
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "worktree", "add", "--detach", path, "HEAD"}})
	if err != nil {
		return "", fmt.Errorf("create fork worktree: %w: %s", err, strings.TrimSpace(string(result.Output)))
	}
	cleanup := func() {
		_, _ = process.RunOutput(context.Background(), process.Spec{Argv: []string{"git", "-C", workspace, "worktree", "remove", "--force", path}})
	}
	diff, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "diff", "--binary", "HEAD"}})
	if err != nil {
		cleanup()
		return "", fmt.Errorf("capture fork changes: %w", err)
	}
	if len(diff.Output) > 0 {
		applied, applyErr := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", path, "apply", "--whitespace=nowarn", "-"}, Stdin: bytes.NewReader(diff.Output)})
		if applyErr != nil {
			cleanup()
			return "", fmt.Errorf("apply fork changes: %w: %s", applyErr, strings.TrimSpace(string(applied.Output)))
		}
	}
	statusOutput, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "status", "--porcelain=v1", "-z", "--untracked-files=all"}})
	if err != nil {
		cleanup()
		return "", fmt.Errorf("inspect fork untracked files: %w", err)
	}
	generatedPrefix := filepath.ToSlash(filepath.Join(".rsch", "worktrees", name))
	for _, relative := range parseUntracked(statusOutput.Output) {
		if relative == generatedPrefix || strings.HasPrefix(relative, generatedPrefix+"/") {
			continue
		}
		if err := copyForkPath(workspace, path, relative); err != nil {
			cleanup()
			return "", err
		}
	}
	return path, nil
}

func (m Manager) confinedPath(workspace, name string) (string, error) {
	path, err := m.Path(name)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = security.ResolveWorkspacePath(workspace, path)
	if err != nil {
		return "", fmt.Errorf("invalid worktree storage path: %w", err)
	}
	return path, nil
}

func parseUntracked(output []byte) []string {
	paths := make([]string, 0)
	records := bytes.Split(output, []byte{0})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 4 || record[2] != ' ' {
			continue
		}
		// A rename/copy has a second NUL-delimited pathname without status
		// columns. Consume it even if its name happens to begin with "?? ".
		if bytes.ContainsAny(record[:2], "RC") {
			index++
			continue
		}
		if string(record[:2]) == "??" {
			paths = append(paths, filepath.ToSlash(string(record[3:])))
		}
	}
	return paths
}

func copyForkPath(workspace, fork, relative string) error {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return fmt.Errorf("unsafe untracked fork path %q", relative)
	}
	source, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenRoot(fork)
	if err != nil {
		return err
	}
	defer target.Close()
	info, err := source.Lstat(clean)
	if err != nil {
		return err
	}
	if err := target.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := source.Readlink(clean)
		if err != nil {
			return err
		}
		return target.Symlink(link, clean)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("unsupported untracked fork path %q", relative)
	}
	input, err := source.Open(clean)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := target.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
func (m Manager) List(ctx context.Context, workspace string) ([]string, error) {
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "worktree", "list", "--porcelain"}})
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(result.Output), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, strings.TrimPrefix(line, "worktree "))
		}
	}
	return paths, nil
}
