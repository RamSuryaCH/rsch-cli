package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ResolveTrustedDataDirectory resolves session/database storage without letting
// an untrusted repository redirect writes outside the workspace. User and
// explicit invocation layers remain trusted; repository layers may set the
// directory only after the workspace was trusted by a user-controlled source.
func ResolveTrustedDataDirectory(root string) (string, error) {
	configured := ""
	trustedWorkspaces := []string{}

	applyTrustedLayer := func(path string, collectTrust bool) error {
		layer, exists, err := readJSONLayer(path)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if value, ok := nestedString(layer, "data", "directory"); ok {
			configured = value
		}
		if collectTrust {
			if values, ok := nestedStrings(layer, "trust", "workspaces"); ok {
				trustedWorkspaces = values
			}
		}
		return nil
	}

	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		if err := applyTrustedLayer(filepath.Join(configDir, "rsch", "config.json"), true); err != nil {
			return "", err
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if err := applyTrustedLayer(filepath.Join(home, ".rsch", "config.json"), true); err != nil {
			return "", err
		}
	}

	workspaceTrusted := isWorkspaceTrusted(root, trustedWorkspaces)
	for _, path := range []string{filepath.Join(root, ".rsch", "config.json"), filepath.Join(root, "rsch.json")} {
		layer, exists, err := readJSONLayer(path)
		if err != nil {
			return "", err
		}
		if !exists || !workspaceTrusted {
			continue
		}
		if value, ok := nestedString(layer, "data", "directory"); ok {
			configured = value
		}
	}

	for _, environment := range []string{"RSCH_SESSION_CONFIG", "RSCH_COMMAND_CONFIG"} {
		if path := strings.TrimSpace(os.Getenv(environment)); path != "" {
			if err := applyTrustedLayer(filepath.Clean(path), false); err != nil {
				return "", fmt.Errorf("load %s: %w", environment, err)
			}
		}
	}
	return ResolveDataDirectory(root, configured), nil
}

func nestedString(layer map[string]any, keys ...string) (string, bool) {
	var current any = layer
	for _, key := range keys {
		mapping, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = caseInsensitiveValue(mapping, key)
		if !ok {
			return "", false
		}
	}
	value, ok := current.(string)
	return strings.TrimSpace(value), ok
}

func nestedStrings(layer map[string]any, keys ...string) ([]string, bool) {
	var current any = layer
	for _, key := range keys {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = caseInsensitiveValue(mapping, key)
		if !ok {
			return nil, false
		}
	}
	values, ok := current.([]any)
	if !ok {
		if stringsValue, stringsOK := current.([]string); stringsOK {
			return append([]string(nil), stringsValue...), true
		}
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result, true
}

func caseInsensitiveValue(mapping map[string]any, key string) (any, bool) {
	for candidate, value := range mapping {
		if strings.EqualFold(strings.TrimSpace(candidate), key) {
			return value, true
		}
	}
	return nil, false
}

// RSCH_TRUSTED_WORKSPACES is an explicit, user-controlled path list. Entries
// must be absolute and match the canonical workspace root exactly. A repository
// cannot set this value through its configuration file.
const trustedWorkspacesEnv = "RSCH_TRUSTED_WORKSPACES"

// Only settings that cannot expand repository authority are accepted before a
// trust decision. Keeping this as an allowlist makes new configuration fields
// protected by default until they receive an explicit security review.
var safeUntrustedWorkspaceSettings = map[string]struct{}{
	"agents":      {},
	"autocompact": {},
	"routing":     {},
	"tui":         {},
}

// WorkspaceTrusted reports the trust decision captured during Load.
func WorkspaceTrusted() bool {
	return cfg != nil && cfg.WorkspaceTrusted
}

func isWorkspaceTrusted(root string, configured []string) bool {
	canonicalRoot, ok := canonicalWorkspacePath(root, false)
	if !ok {
		return false
	}
	candidates := append([]string(nil), configured...)
	candidates = append(candidates, filepath.SplitList(os.Getenv(trustedWorkspacesEnv))...)
	for _, candidate := range candidates {
		canonicalCandidate, valid := canonicalWorkspacePath(candidate, true)
		if valid && sameWorkspacePath(canonicalRoot, canonicalCandidate) {
			return true
		}
	}
	return false
}

func canonicalWorkspacePath(path string, requireAbsolute bool) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || requireAbsolute && !filepath.IsAbs(path) {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = filepath.Clean(resolved)
	}
	return absolute, true
}

func sameWorkspacePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func filterWorkspaceConfig(layer map[string]any, trusted bool) (map[string]any, []string) {
	if trusted {
		return layer, nil
	}
	filtered := make(map[string]any, len(layer))
	blocked := make([]string, 0, len(layer))
	for key, value := range layer {
		if _, safe := safeUntrustedWorkspaceSettings[strings.ToLower(strings.TrimSpace(key))]; safe {
			filtered[key] = value
			continue
		}
		blocked = append(blocked, key)
	}
	sort.Strings(blocked)
	return filtered, blocked
}
