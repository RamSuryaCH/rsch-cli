// Package gitcommit provides preview-first, staged-only Git commit helpers.
package gitcommit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/process"
)

type Preview struct {
	Message string   `json:"message"`
	Files   []string `json:"files"`
	Stat    string   `json:"stat,omitempty"`
}

func PreviewStaged(ctx context.Context, workspace string) (Preview, error) {
	nameOutput, err := git(ctx, workspace, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return Preview{}, err
	}
	if nameOutput == "" {
		return Preview{}, errors.New("no staged changes to commit")
	}
	files := []string{}
	for _, path := range strings.Split(nameOutput, "\x00") {
		if path == "" {
			continue
		}
		if unsafe(path) {
			return Preview{}, fmt.Errorf("refusing smart commit for sensitive path %q", path)
		}
		files = append(files, path)
	}
	if len(files) == 0 {
		return Preview{}, errors.New("staged changes did not contain file paths")
	}
	sort.Strings(files)
	stat, _ := git(ctx, workspace, "diff", "--cached", "--stat")
	return Preview{Message: inferMessage(files), Files: files, Stat: strings.TrimSpace(stat)}, nil
}

func Commit(ctx context.Context, workspace string, preview Preview, message string) error {
	if strings.TrimSpace(message) == "" {
		message = preview.Message
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("commit message is required")
	}
	_, err := git(ctx, workspace, "commit", "-m", message)
	return err
}

func inferMessage(files []string) string {
	typeName := "feat"
	for _, path := range files {
		lower := strings.ToLower(path)
		switch {
		case strings.Contains(lower, "test") || strings.HasSuffix(lower, "_test.go"):
			typeName = "test"
		case strings.HasSuffix(lower, ".md") || strings.Contains(lower, "docs"):
			typeName = "docs"
		case strings.Contains(lower, "fix") || strings.Contains(lower, "bug"):
			typeName = "fix"
		case strings.Contains(lower, "config") || strings.Contains(lower, "dependency"):
			typeName = "chore"
		}
	}
	component := commonComponent(files)
	description := "update project files"
	if len(files) == 1 {
		description = "update " + strings.TrimSuffix(files[0], "/")
	}
	if component != "" {
		return fmt.Sprintf("%s(%s): %s", typeName, component, description)
	}
	return typeName + ": " + description
}

func commonComponent(files []string) string {
	if len(files) == 0 {
		return ""
	}
	parts := strings.Split(files[0], "/")
	if len(parts) < 2 || parts[0] == "cmd" || parts[0] == "internal" {
		if len(parts) > 1 {
			return parts[1]
		}
		return ""
	}
	return parts[0]
}

func unsafe(path string) bool {
	lower := strings.ToLower(path)
	for _, component := range strings.Split(lower, "/") {
		if strings.HasPrefix(component, ".env") {
			return true
		}
	}
	return strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key")
}

func git(ctx context.Context, workspace string, args ...string) (string, error) {
	result, err := process.RunOutput(ctx, process.Spec{Argv: append([]string{"git", "-C", workspace}, args...)})
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(result.Output)))
	}
	return string(result.Output), nil
}
