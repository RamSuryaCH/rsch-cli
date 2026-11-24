package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveWorkspacePath returns a canonical path that is contained by root.
// It checks existing symlinks and the nearest existing parent for new files,
// preventing both sibling-prefix confusion and symlink escapes.
func ResolveWorkspacePath(root, target string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(target) == "" {
		return "", errors.New("workspace root and target are required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	targetAbs := target
	if !filepath.IsAbs(targetAbs) {
		targetAbs = filepath.Join(rootAbs, targetAbs)
	}
	targetAbs, err = filepath.Abs(targetAbs)
	if err != nil {
		return "", err
	}
	resolved := targetAbs
	if _, statErr := os.Lstat(targetAbs); statErr == nil {
		resolved, err = filepath.EvalSymlinks(targetAbs)
		if err != nil {
			return "", err
		}
	} else if os.IsNotExist(statErr) {
		probe := filepath.Dir(targetAbs)
		parts := []string{filepath.Base(targetAbs)}
		for {
			// Only skip genuinely missing components. An existing symlink
			// whose target cannot be resolved must never become a lexical
			// component of an apparently safe path.
			_, parentErr := os.Lstat(probe)
			if parentErr == nil {
				parent, resolveErr := filepath.EvalSymlinks(probe)
				if resolveErr != nil {
					return "", resolveErr
				}
				resolved = parent
				for i := len(parts) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, parts[i])
				}
				break
			}
			if !os.IsNotExist(parentErr) {
				return "", parentErr
			}
			next := filepath.Dir(probe)
			if next == probe {
				return "", parentErr
			}
			parts = append(parts, filepath.Base(probe))
			probe = next
		}
	} else {
		return "", statErr
	}
	rel, err := filepath.Rel(rootAbs, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes the workspace")
	}
	return filepath.Clean(resolved), nil
}
