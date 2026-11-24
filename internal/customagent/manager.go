// Package customagent manages repository-defined agent manifests.
package customagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/project"
)

type Entry struct {
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	Definition Definition `json:"definition"`
}

// Definition is the filesystem-native contract for an agent. Manifest holds
// the execution-facing fields shared with other project resources; the
// additional fields describe delegation without coupling the manifest to a
// runtime implementation.
type Definition struct {
	project.Manifest
	Description    string        `json:"description,omitempty"`
	Subagents      []string      `json:"subagents,omitempty"`
	Memory         []string      `json:"memory,omitempty"`
	Reviewers      []string      `json:"reviewers,omitempty"`
	MaxDepth       int           `json:"maxDepth,omitempty"`
	MaxAgents      int           `json:"maxAgents,omitempty"`
	MaxCost        float64       `json:"maxCost,omitempty"`
	MaxRuntime     time.Duration `json:"maxRuntime,omitempty"`
	MaxConcurrency int           `json:"maxConcurrency,omitempty"`
}

// Validate enforces the local declarative contract before an agent can be
// discovered or selected. Limits are optional (zero means unspecified), but
// negative values and recursive self-delegation are never valid.
func (d Definition) Validate(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("custom agent name is required")
	}
	if d.ID == "" || d.Name == "" || d.Version == "" || strings.TrimSpace(d.Role) == "" {
		return errors.New("custom agent manifest requires id, name, version, and role")
	}
	if d.Name != name {
		return fmt.Errorf("custom agent manifest name %q does not match file name %q", d.Name, name)
	}
	if d.MaxDepth < 0 || d.MaxAgents < 0 || d.MaxCost < 0 || d.MaxRuntime < 0 || d.MaxConcurrency < 0 {
		return errors.New("custom agent delegation limits cannot be negative")
	}
	if d.Budget < 0 || d.Timeout < 0 {
		return errors.New("custom agent budget and timeout cannot be negative")
	}
	if d.MaxAgents > 0 && d.MaxConcurrency > d.MaxAgents {
		return errors.New("custom agent maxConcurrency cannot exceed maxAgents")
	}
	if err := validateNames("subagent", name, d.Subagents); err != nil {
		return err
	}
	if err := validateNames("reviewer", "", d.Reviewers); err != nil {
		return err
	}
	return nil
}

func validateNames(kind, self string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("custom agent %s name cannot be empty", kind)
		}
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("custom agent %s name %q cannot contain surrounding whitespace", kind, value)
		}
		if value == self {
			return fmt.Errorf("custom agent cannot delegate to itself")
		}
		if err := validateName(value); err != nil {
			return fmt.Errorf("invalid custom agent %s %q: %w", kind, value, err)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("custom agent %s %q is repeated", kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

type Manager struct{ Root string }

func New(root string) (Manager, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Manager{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Manager{}, err
	}
	if !info.IsDir() {
		return Manager{}, errors.New("agent workspace is not a directory")
	}
	return Manager{Root: absolute}, nil
}

func validateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid agent name %q", name)
	}
	return nil
}

func (m Manager) path(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	return filepath.Join(m.Root, ".rsch", "agents", name+".json"), nil
}

// catalogRoot keeps registry IO anchored to the workspace even when catalog
// paths are replaced with symlinks while an operation is in progress.
func (m Manager) catalogRoot(create bool) (*os.Root, error) {
	workspace, err := os.OpenRoot(m.Root)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	directory := filepath.Join(".rsch", "agents")
	if create {
		if err := workspace.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	return workspace.OpenRoot(directory)
}

func (m Manager) List() ([]Entry, error) {
	root, err := m.catalogRoot(false)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	items, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	result := []Entry{}
	for _, item := range items {
		if item.IsDir() || strings.ToLower(filepath.Ext(item.Name())) != ".json" {
			continue
		}
		name := strings.TrimSuffix(item.Name(), filepath.Ext(item.Name()))
		entry, err := m.Inspect(name)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (m Manager) Inspect(name string) (Entry, error) {
	path, err := m.path(name)
	if err != nil {
		return Entry{}, err
	}
	root, err := m.catalogRoot(false)
	if err != nil {
		return Entry{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		return Entry{}, err
	}
	var definition Definition
	if err := json.Unmarshal(data, &definition); err != nil {
		return Entry{}, fmt.Errorf("invalid custom agent manifest: %w", err)
	}
	if err := definition.Validate(name); err != nil {
		return Entry{}, err
	}
	return Entry{Name: name, Path: path, Definition: definition}, nil
}

func (m Manager) Create(name string, definition project.Manifest) (string, error) {
	return m.CreateDefinition(name, Definition{Manifest: definition})
}

// CreateDefinition writes a complete declarative agent contract. JSON is
// intentionally used as the canonical format so the manifest remains
// inspectable by the existing project-resource discovery code.
func (m Manager) CreateDefinition(name string, definition Definition) (string, error) {
	path, err := m.path(name)
	if err != nil {
		return "", err
	}
	if definition.ID == "" {
		definition.ID = name
	}
	if definition.Name == "" {
		definition.Name = name
	}
	if definition.Version == "" {
		definition.Version = "1"
	}
	if err := definition.Validate(name); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(definition, "", "  ")
	if err != nil {
		return "", err
	}
	root, err := m.catalogRoot(true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return "", fmt.Errorf("custom agent %q already exists", name)
	}
	if err != nil {
		return "", err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

func (m Manager) Remove(name string) error {
	path, err := m.path(name)
	if err != nil {
		return err
	}
	root, err := m.catalogRoot(false)
	if os.IsNotExist(err) {
		return fmt.Errorf("custom agent %q is not installed", name)
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(filepath.Base(path)); os.IsNotExist(err) {
		return fmt.Errorf("custom agent %q is not installed", name)
	} else {
		return err
	}
}
