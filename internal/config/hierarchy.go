package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HierarchyPaths returns configuration files in ascending precedence order.
// Missing files are intentionally retained in the result so callers can show
// the complete search path to users.
func HierarchyPaths(root string) []string {
	paths := make([]string, 0, 4)
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		paths = append(paths, filepath.Join(configDir, "rsch", "config.json"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".rsch", "config.json"))
	}
	if root != "" {
		paths = append(paths, filepath.Join(root, ".rsch", "config.json"), filepath.Join(root, "rsch.json"))
	}
	// Session and command layers are explicit opt-in overrides. Keeping their
	// paths environment-driven avoids inventing hidden state while allowing
	// CI, editor integrations, and a single invocation to override a project.
	if session := strings.TrimSpace(os.Getenv("RSCH_SESSION_CONFIG")); session != "" {
		paths = append(paths, filepath.Clean(session))
	}
	if command := strings.TrimSpace(os.Getenv("RSCH_COMMAND_CONFIG")); command != "" {
		paths = append(paths, filepath.Clean(command))
	}
	return paths
}

// LoadHierarchy merges configuration from lowest to highest precedence:
// global, organization, repository, directory, session, and command.
func LoadHierarchy(paths ...string) (map[string]any, error) {
	result := make(map[string]any)
	for _, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var layer map[string]any
		if err := json.Unmarshal(data, &layer); err != nil {
			return nil, fmt.Errorf("invalid configuration %s: %w", path, err)
		}
		mergeMaps(result, layer)
	}
	return result, nil
}
func mergeMaps(dst, src map[string]any) {
	for key, value := range src {
		source, ok := value.(map[string]any)
		if !ok {
			dst[key] = value
			continue
		}
		target, ok := dst[key].(map[string]any)
		if !ok {
			target = make(map[string]any)
			dst[key] = target
		}
		mergeMaps(target, source)
	}
}
