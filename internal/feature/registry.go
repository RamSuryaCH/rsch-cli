// Package feature manages the repository feature registry. A feature can only
// be marked verified when it carries at least one concrete verification item.
package feature

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Status string

const (
	Planned      Status = "planned"
	Designing    Status = "designing"
	Implementing Status = "implementing"
	Testing      Status = "testing"
	Verified     Status = "verified"
	Released     Status = "released"
	Deprecated   Status = "deprecated"
)

type Entry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       Status   `json:"status"`
	Priority     string   `json:"priority,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	Verification []string `json:"verification,omitempty"`
}

type Registry struct {
	Version  int     `json:"version"`
	Features []Entry `json:"features"`
}

var validStatuses = map[Status]bool{Planned: true, Designing: true, Implementing: true, Testing: true, Verified: true, Released: true, Deprecated: true}

func Path(root string) string {
	return filepath.Join(root, ".rsch", "product", "features", "registry.json")
}

func Load(root string) (Registry, error) {
	data, err := os.ReadFile(Path(root))
	if err != nil {
		return Registry{}, err
	}
	var registry Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return Registry{}, fmt.Errorf("invalid feature registry: %w", err)
	}
	if err := ValidateAt(root, registry); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func Save(root string, registry Registry) error {
	if registry.Version == 0 {
		registry.Version = 1
	}
	if err := ValidateAt(root, registry); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Path(root)), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(root), append(data, '\n'), 0o600)
}

// ValidateAt proves that every declared verification reference is present in
// the repository, preventing nonexistent files from being used as evidence.
func ValidateAt(root string, registry Registry) error {
	if err := Validate(registry); err != nil {
		return err
	}
	for _, item := range registry.Features {
		for _, reference := range item.Verification {
			if strings.TrimSpace(reference) == "" {
				return fmt.Errorf("feature %s has an empty verification reference", item.ID)
			}
			if err := validateVerificationFile(root, reference); err != nil {
				return fmt.Errorf("feature %s verification reference %q is unavailable: %w", item.ID, reference, err)
			}
		}
	}
	return nil
}

func validateVerificationFile(root, reference string) error {
	path := filepath.FromSlash(reference)
	if !filepath.IsLocal(path) {
		return errors.New("verification evidence must be a relative path inside the repository")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, path))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("verification evidence resolves outside the repository")
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("verification evidence must be a regular file")
	}
	return nil
}

func Validate(registry Registry) error {
	if registry.Version <= 0 {
		return errors.New("feature registry version must be positive")
	}
	seen := map[string]bool{}
	for _, item := range registry.Features {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" {
			return errors.New("feature registry entries require id and name")
		}
		if seen[item.ID] {
			return fmt.Errorf("duplicate feature id %q", item.ID)
		}
		seen[item.ID] = true
		if !validStatuses[item.Status] {
			return fmt.Errorf("invalid status %q for feature %s", item.Status, item.ID)
		}
		if (item.Status == Verified || item.Status == Released) && len(item.Verification) == 0 {
			return fmt.Errorf("feature %s cannot be %s without verification evidence", item.ID, item.Status)
		}
	}
	for _, item := range registry.Features {
		for _, dep := range item.Dependencies {
			if !seen[dep] {
				return fmt.Errorf("feature %s depends on unknown feature %s", item.ID, dep)
			}
		}
	}
	return nil
}

func Sort(registry Registry) Registry {
	sort.Slice(registry.Features, func(i, j int) bool { return registry.Features[i].ID < registry.Features[j].ID })
	return registry
}
