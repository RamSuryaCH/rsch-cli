// Package spec manages repository-local, reviewable engineering specifications.
package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var required = []string{"requirements.md", "design.md", "architecture.md", "tasks.md", "decisions.md", "validation.md"}
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type Manager struct{ Root string }

func NewManager(workspace string) Manager {
	return Manager{Root: filepath.Join(workspace, ".rsch", "specs")}
}
func (m Manager) Path(name string) (string, error) {
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid feature name %q", name)
	}
	return filepath.Join(m.Root, name), nil
}
func (m Manager) New(name string) (string, error) {
	path, err := m.Path(name)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(path); err == nil {
		return "", fmt.Errorf("spec %q already exists", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err = os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	headings := map[string]string{"requirements.md": "# Requirements\n\n## Problem\n\n## Acceptance criteria\n", "design.md": "# Design\n\n## Scope\n\n## Non-goals\n", "architecture.md": "# Architecture\n\n## Impact\n\n## Data and API changes\n", "tasks.md": "# Tasks\n\n## Dependency graph\n", "decisions.md": "# Decisions\n\n## Decision log\n", "validation.md": "# Validation\n\n## Verification evidence\n"}
	for _, file := range required {
		if err := os.WriteFile(filepath.Join(path, file), []byte(headings[file]), 0o600); err != nil {
			return "", err
		}
	}
	plan, _ := json.MarshalIndent(Plan{Version: 1, Tasks: []PlanTask{}}, "", "  ")
	if err := os.WriteFile(filepath.Join(path, planFile), append(plan, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
func (m Manager) Validate(name string) error {
	path, err := m.Path(name)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("spec path is not a directory")
	}
	var missing []string
	for _, file := range required {
		if info, err := os.Stat(filepath.Join(path, file)); err != nil || info.IsDir() {
			missing = append(missing, file)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("spec %q missing: %s", name, strings.Join(missing, ", "))
	}
	return nil
}
func (m Manager) Show(name string) (map[string]string, error) {
	if err := m.Validate(name); err != nil {
		return nil, err
	}
	path, _ := m.Path(name)
	out := make(map[string]string, len(required))
	for _, file := range required {
		data, err := os.ReadFile(filepath.Join(path, file))
		if err != nil {
			return nil, err
		}
		out[file] = string(data)
	}
	return out, nil
}
func (m Manager) Archive(name string) (string, error) {
	path, err := m.Path(name)
	if err != nil {
		return "", err
	}
	if err := m.Validate(name); err != nil {
		return "", err
	}
	archive := filepath.Join(m.Root, "archive")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(archive, fmt.Sprintf("%s-%s", name, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}
func RequiredFiles() []string { return append([]string(nil), required...) }
func SortFiles(files []string) []string {
	out := append([]string(nil), files...)
	sort.Strings(out)
	return out
}
