// Package infrastructure extracts deployment and infrastructure facts from a
// repository without executing provider-specific tooling.
package infrastructure

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Resource struct {
	Kind       string            `json:"kind"`
	Name       string            `json:"name,omitempty"`
	Path       string            `json:"path"`
	Line       int               `json:"line"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type Report struct {
	Root      string     `json:"root"`
	Resources []Resource `json:"resources"`
}

func Scan(ctx context.Context, root string) (Report, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, errors.New("infrastructure root is not a directory")
	}
	report := Report{Root: absolute, Resources: []Resource{}}
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != absolute && ignored(filepath.Base(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		kind := classify(filepath.Base(path))
		if kind == "" {
			return nil
		}
		items, parseErr := parseFile(absolute, path, kind)
		if parseErr != nil {
			return parseErr
		}
		report.Resources = append(report.Resources, items...)
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Resources, func(i, j int) bool {
		if report.Resources[i].Path == report.Resources[j].Path {
			return report.Resources[i].Line < report.Resources[j].Line
		}
		return report.Resources[i].Path < report.Resources[j].Path
	})
	return report, nil
}

func classify(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "dockerfile" || strings.HasPrefix(lower, "dockerfile."):
		return "docker"
	case strings.Contains(lower, "compose") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")):
		return "compose"
	case strings.HasSuffix(lower, ".tf") || strings.HasSuffix(lower, ".tf.json"):
		return "terraform"
	case strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml"):
		return "kubernetes"
	default:
		return ""
	}
}

func parseFile(root, path, kind string) ([]Resource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	items := []Resource{}
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "//") {
			continue
		}
		name, attributes := fact(kind, text)
		if name == "" {
			continue
		}
		items = append(items, Resource{Kind: kind, Name: name, Path: rel, Line: line, Attributes: attributes})
	}
	return items, scanner.Err()
}

func fact(kind, text string) (string, map[string]string) {
	switch kind {
	case "docker":
		parts := strings.Fields(text)
		if len(parts) == 0 {
			return "", nil
		}
		switch strings.ToUpper(parts[0]) {
		case "FROM":
			index := 1
			for index < len(parts) && strings.HasPrefix(parts[index], "--") {
				index++
			}
			if index < len(parts) {
				return parts[index], map[string]string{"directive": "FROM"}
			}
		case "EXPOSE":
			if len(parts) > 1 {
				return parts[1], map[string]string{"directive": "EXPOSE"}
			}
		case "ENTRYPOINT", "CMD":
			if len(parts) > 1 {
				return strings.Join(parts[1:], " "), map[string]string{"directive": strings.ToUpper(parts[0])}
			}
		}
	case "terraform":
		parts := strings.Fields(text)
		if len(parts) >= 2 && (parts[0] == "module" || parts[0] == "provider") {
			return strings.Trim(parts[1], "\""), map[string]string{"block": parts[0]}
		}
		if len(parts) >= 3 && (parts[0] == "resource" || parts[0] == "data") {
			return strings.Trim(parts[2], "\"") + ":" + strings.Trim(parts[1], "\""), map[string]string{"block": parts[0]}
		}
	case "kubernetes", "compose":
		if strings.HasPrefix(text, "kind:") {
			return strings.TrimSpace(strings.TrimPrefix(text, "kind:")), map[string]string{"field": "kind"}
		}
		if strings.HasPrefix(text, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(text, "name:")), map[string]string{"field": "name"}
		}
		if strings.HasPrefix(text, "image:") {
			return strings.TrimSpace(strings.TrimPrefix(text, "image:")), map[string]string{"field": "image"}
		}
	}
	return "", nil
}

func ignored(name string) bool {
	switch name {
	case ".git", ".rsch", "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}
