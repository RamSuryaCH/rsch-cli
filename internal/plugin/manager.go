// Package plugin provides the versioned, policy-neutral plugin registry.
// Execution is deliberately delegated to project.Runtime so registering a
// plugin cannot create a second security boundary.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opencode-ai/opencode/internal/project"
)

const APIVersion = "v1"

var Capabilities = []string{"commands", "agents", "tools", "skills", "hooks", "models", "providers", "memory", "verification", "notifications", "ui"}

type Entry struct {
	Name       string           `json:"name"`
	Path       string           `json:"path"`
	APIVersion string           `json:"apiVersion"`
	Definition project.Manifest `json:"definition"`
	Descriptor Descriptor       `json:"descriptor"`
}

// ExtensionDescriptor is the stable, client-independent declaration exposed
// by a plugin. It describes what the plugin contributes; execution remains
// delegated to project.Runtime.
type ExtensionDescriptor struct {
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities,omitempty"`
	Permissions  []string `json:"permissions,omitempty"`
}

type Descriptor struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	APIVersion   string                `json:"apiVersion"`
	Capabilities []string              `json:"capabilities,omitempty"`
	Permissions  []string              `json:"permissions,omitempty"`
	Extensions   []ExtensionDescriptor `json:"extensions,omitempty"`
}

type Manager struct{ Root string }

func New(root string) (Manager, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Manager{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Manager{}, err
	}
	if !info.IsDir() {
		return Manager{}, errors.New("plugin workspace is not a directory")
	}
	return Manager{Root: abs}, nil
}

func (m Manager) path(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("invalid plugin name %q", name)
	}
	return filepath.Join(m.Root, ".rsch", "plugins", name+".json"), nil
}

// catalogRoot keeps registry IO anchored to the workspace even when catalog
// paths are replaced with symlinks while an operation is in progress.
func (m Manager) catalogRoot(create bool) (*os.Root, error) {
	workspace, err := os.OpenRoot(m.Root)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	directory := filepath.Join(".rsch", "plugins")
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
	result := make([]Entry, 0)
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
	var definition project.Manifest
	if err := json.Unmarshal(data, &definition); err != nil {
		return Entry{}, fmt.Errorf("invalid plugin manifest: %w", err)
	}
	if err := ValidateManifest(definition); err != nil {
		return Entry{}, err
	}
	descriptor := Describe(definition)
	return Entry{Name: name, Path: path, APIVersion: APIVersion, Definition: definition, Descriptor: descriptor}, nil
}

func ValidateManifest(definition project.Manifest) error {
	if strings.TrimSpace(definition.ID) == "" || strings.TrimSpace(definition.Name) == "" || strings.TrimSpace(definition.Version) == "" || strings.TrimSpace(definition.Entrypoint) == "" {
		return errors.New("plugin manifest requires id, name, version, and entrypoint")
	}
	if definition.APIVersion != "" && definition.APIVersion != APIVersion {
		return fmt.Errorf("unsupported plugin API version %q", definition.APIVersion)
	}
	if definition.Budget < 0 || definition.Timeout < 0 {
		return errors.New("plugin budget and timeout cannot be negative")
	}
	seen := make(map[string]bool, len(definition.Capabilities))
	for _, capability := range definition.Capabilities {
		if strings.TrimSpace(capability) == "" {
			return errors.New("plugin capabilities cannot contain empty values")
		}
		if seen[capability] {
			return fmt.Errorf("plugin repeats capability %q", capability)
		}
		seen[capability] = true
		if !contains(Capabilities, capability) {
			return fmt.Errorf("unsupported plugin capability %q", capability)
		}
	}
	for _, permission := range definition.Permissions {
		if strings.TrimSpace(permission) == "" {
			return errors.New("plugin permissions cannot contain empty values")
		}
	}
	return nil
}

func Describe(definition project.Manifest) Descriptor {
	capabilities := append([]string(nil), definition.Capabilities...)
	permissions := append([]string(nil), definition.Permissions...)
	sort.Strings(capabilities)
	sort.Strings(permissions)
	extensions := make([]ExtensionDescriptor, 0, len(capabilities))
	for _, capability := range capabilities {
		extensions = append(extensions, ExtensionDescriptor{Kind: capability, Name: definition.Name, Version: definition.Version, Capabilities: []string{capability}, Permissions: permissions})
	}
	return Descriptor{ID: definition.ID, Name: definition.Name, Version: definition.Version, APIVersion: APIVersion, Capabilities: capabilities, Permissions: permissions, Extensions: extensions}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Run executes a registered plugin through the supplied project runtime. The
// plugin manager does not own a process or permission boundary.
func (m Manager) Run(ctx context.Context, name string, runtime project.Runtime, invocation project.Invocation) (project.Result, error) {
	entry, err := m.Inspect(name)
	if err != nil {
		return project.Result{}, err
	}
	invocation.ResourceID = entry.Definition.ID
	invocation.Kind = "plugin"
	return runtime.Run(ctx, project.Resource{Kind: "plugin", Path: entry.Path, Manifest: &entry.Definition}, invocation)
}

// Invoke dispatches one declared plugin extension through the shared project
// runtime. The capability must be declared by the plugin and is checked again
// by the runtime's governance policy before any process can start.
func (m Manager) Invoke(ctx context.Context, name, capability string, runtime project.Runtime, arguments []string, eventJSON string) (project.Result, error) {
	capability = strings.TrimSpace(capability)
	if !contains(Capabilities, capability) {
		return project.Result{}, fmt.Errorf("unsupported plugin capability %q", capability)
	}
	entry, err := m.Inspect(name)
	if err != nil {
		return project.Result{}, err
	}
	if !contains(entry.Descriptor.Capabilities, capability) {
		return project.Result{}, fmt.Errorf("plugin %q does not declare capability %q", name, capability)
	}
	return m.Run(ctx, name, runtime, project.Invocation{Capability: capability, Arguments: arguments, EventJSON: eventJSON})
}

func (m Manager) Install(name, source string) (string, error) {
	destination, err := m.path(name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(source) == "" {
		return "", errors.New("plugin source is required")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	var definition project.Manifest
	if err := json.Unmarshal(data, &definition); err != nil {
		return "", fmt.Errorf("invalid plugin manifest: %w", err)
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
	if definition.APIVersion == "" {
		definition.APIVersion = APIVersion
	}
	if definition.Entrypoint == "" {
		return "", errors.New("plugin entrypoint is required")
	}
	if err := ValidateManifest(definition); err != nil {
		return "", err
	}
	data, err = json.MarshalIndent(definition, "", "  ")
	if err != nil {
		return "", err
	}
	root, err := m.catalogRoot(true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(destination), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return "", fmt.Errorf("plugin %q already exists", name)
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
	return destination, nil
}

func (m Manager) Remove(name string) error {
	path, err := m.path(name)
	if err != nil {
		return err
	}
	root, err := m.catalogRoot(false)
	if os.IsNotExist(err) {
		return fmt.Errorf("plugin %q is not installed", name)
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(filepath.Base(path)); os.IsNotExist(err) {
		return fmt.Errorf("plugin %q is not installed", name)
	} else {
		return err
	}
}
