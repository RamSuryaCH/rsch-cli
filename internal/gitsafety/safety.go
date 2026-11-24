// Package gitsafety contains read-only Git inspection and collision checks.
package gitsafety

import (
	"context"
	"errors"
	"strings"

	"github.com/opencode-ai/opencode/internal/process"
)

type Status struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Branch    string   `json:"branch,omitempty"`
	Clean     bool     `json:"clean"`
	Entries   []string `json:"entries"`
}

func Inspect(ctx context.Context, workspace string) (Status, error) {
	if !process.Exists("git") {
		return Status{Reason: "git executable is unavailable"}, nil
	}
	probe, probeErr := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "rev-parse", "--is-inside-work-tree"}})
	if probeErr != nil || strings.TrimSpace(string(probe.Output)) != "true" {
		return Status{Reason: "current workspace is not a Git repository"}, nil
	}
	result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "status", "--porcelain=v1", "--untracked-files=all"}})
	if err != nil {
		return Status{}, err
	}
	branchResult, _ := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", workspace, "branch", "--show-current"}})
	branch := strings.TrimSpace(string(branchResult.Output))
	text := strings.TrimSpace(string(result.Output))
	if text == "" {
		return Status{Available: true, Branch: branch, Clean: true}, nil
	}
	lines := strings.Split(text, "\n")
	return Status{Available: true, Branch: branch, Entries: lines}, nil
}
func RequireClean(ctx context.Context, workspace string) error {
	status, err := Inspect(ctx, workspace)
	if err != nil {
		return err
	}
	if !status.Available {
		return errors.New("Git repository: unavailable\nReason: " + status.Reason)
	}
	if !status.Clean {
		return errors.New("workspace has uncommitted or untracked changes")
	}
	return nil
}
func Overlap(pathsA, pathsB []string) []string {
	set := map[string]bool{}
	for _, path := range pathsA {
		set[path] = true
	}
	var overlap []string
	for _, path := range pathsB {
		if set[path] {
			overlap = append(overlap, path)
		}
	}
	return overlap
}
