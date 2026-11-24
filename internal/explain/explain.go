// Package explain produces concise, evidence-backed repository explanations.
// It never exposes model reasoning or claims beyond facts found locally.
package explain

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/security"
)

type Report struct {
	Target       string   `json:"target"`
	Purpose      string   `json:"purpose,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	Callers      []string `json:"callers,omitempty"`
	Behavior     []string `json:"importantBehavior,omitempty"`
	Risks        []string `json:"potentialRisks,omitempty"`
	Tests        []string `json:"tests,omitempty"`
	RecentChange []string `json:"recentChanges,omitempty"`
}

// Run explains a repository or a file using source, test, and Git evidence.
func Run(ctx context.Context, root, target string) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	absolute, err := security.ResolveWorkspacePath(root, ".")
	if err != nil {
		return Report{}, err
	}
	if target == "" {
		target = "."
	}
	target = strings.TrimSpace(strings.TrimPrefix(target, "file "))
	path, err := security.ResolveWorkspacePath(absolute, filepath.FromSlash(target))
	if err != nil {
		return Report{}, fmt.Errorf("resolve explain target: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Report{}, err
	}
	relative, _ := filepath.Rel(absolute, path)
	relative = filepath.ToSlash(relative)
	report := Report{Target: relative}
	if info.IsDir() {
		report.Purpose = "repository workspace"
		if readme, resolveErr := security.ResolveWorkspacePath(absolute, filepath.Join(path, "README.md")); resolveErr == nil {
			if info, statErr := os.Stat(readme); statErr == nil && info.Mode().IsRegular() {
				if data, readErr := os.ReadFile(readme); readErr == nil {
					report.Purpose = firstHeading(string(data))
				}
			}
		}
		report.RecentChange = gitLog(ctx, absolute, "--", relative)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		return report, nil
	}
	if !info.Mode().IsRegular() {
		return Report{}, errors.New("explain target is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, err
	}
	report.Purpose = purpose(string(data))
	report.Dependencies = dependencies(string(data))
	report.Behavior = behavior(string(data))
	report.Risks = risks(relative, string(data))
	report.Tests, err = findTests(ctx, absolute, relative)
	if err != nil {
		return Report{}, err
	}
	report.Callers, err = findCallers(ctx, absolute, relative, string(data))
	if err != nil {
		return Report{}, err
	}
	report.RecentChange = gitLog(ctx, absolute, "--", relative)
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func purpose(data string) string {
	if heading := firstHeading(data); heading != "" {
		return heading
	}
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "//"))
		}
		if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#!") {
			return strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
	}
	return "source file"
}

func firstHeading(data string) string {
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#!") {
			return strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
	}
	return ""
}

var importPattern = regexp.MustCompile(`(?:import\s*\(?\s*|require\s*\(\s*)["']([^"']+)["']`)
var quotedImportPattern = regexp.MustCompile(`(?m)^\s*["']([^"']+)["']`)

func dependencies(data string) []string {
	seen := map[string]bool{}
	for _, match := range importPattern.FindAllStringSubmatch(data, -1) {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
		}
	}
	for _, match := range quotedImportPattern.FindAllStringSubmatch(data, -1) {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for item := range seen {
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func behavior(data string) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() && len(out) < 8 {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "func ") || strings.HasPrefix(line, "function ") || strings.HasPrefix(line, "def ") || strings.HasPrefix(line, "type ") || strings.HasPrefix(line, "class ") {
			out = append(out, line)
		}
	}
	return out
}

func risks(path, data string) []string {
	lower := strings.ToLower(path + "\n" + data)
	var out []string
	for _, item := range []struct{ needle, message string }{
		{"auth", "authentication or authorization behavior requires review"},
		{"security", "security-sensitive behavior requires review"},
		{"password", "credential handling requires secret-safety review"},
		{"token", "token handling requires secret-safety review"},
		{"migration", "schema or migration changes require compatibility review"},
		{"exec.", "process execution requires policy and sandbox review"},
	} {
		if strings.Contains(lower, item.needle) {
			out = append(out, item.message)
		}
	}
	return out
}

func findTests(ctx context.Context, root, target string) ([]string, error) {
	base := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	var out []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.IsDir() {
			if entry != nil && entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".rsch" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		if strings.Contains(name, base) && strings.Contains(name, "test") {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func findCallers(ctx context.Context, root, target, data string) ([]string, error) {
	base := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	name := base
	if match := regexp.MustCompile(`(?:func|function|def)\s+([A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(data); len(match) > 1 {
		name = match[1]
	}
	callPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	var out []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.IsDir() {
			if entry != nil && entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".rsch" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return walkErr
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == target || !entry.Type().IsRegular() || !isSource(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || !callPattern.Match(data) {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func isSource(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt", ".rb", ".php", ".swift":
		return true
	}
	return false
}

func gitLog(ctx context.Context, root string, args ...string) []string {
	commandArgs := append([]string{"-C", root, "log", "-5", "--pretty=format:%h %s (%ad)", "--date=short"}, args...)
	result, err := process.RunOutput(ctx, process.Spec{Argv: append([]string{"git"}, commandArgs...)})
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(result.Output)), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
