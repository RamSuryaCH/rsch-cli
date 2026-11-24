// Package steering resolves project rules in an explicit, inspectable order.
package steering

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
)

type Scope string

const (
	Global       Scope = "global"
	Organization Scope = "organization"
	Repository   Scope = "repository"
	Directory    Scope = "directory"
	Session      Scope = "session"
	Task         Scope = "task"
)

type Rule struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Scope    Scope  `json:"scope"`
	Priority int    `json:"priority"`
	Content  string `json:"content"`
}
type Set struct {
	Rules    []Rule `json:"rules"`
	Combined string `json:"combined"`
}

var precedence = map[Scope]int{Global: 10, Organization: 20, Repository: 30, Directory: 40, Session: 50, Task: 60}

func Load(root string) (Set, error) {
	if strings.TrimSpace(root) == "" {
		return Set{}, errors.New("steering root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Set{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return Set{}, err
	}
	if !info.IsDir() {
		return Set{}, errors.New("steering root is not a directory")
	}
	workspace, err := os.OpenRoot(absolute)
	if err != nil {
		return Set{}, err
	}
	defer workspace.Close()
	rules := []Rule{}
	if configDir, configErr := os.UserConfigDir(); configErr == nil {
		rules, err = appendRules(rules, filepath.Join(configDir, "rsch", "steering"), Global, nil)
		if err != nil {
			return Set{}, err
		}
	}
	if current, currentErr := user.Current(); currentErr == nil && current.HomeDir != "" {
		rules, err = appendRules(rules, filepath.Join(current.HomeDir, ".rsch", "steering"), Organization, nil)
		if err != nil {
			return Set{}, err
		}
	}
	rules, err = appendRules(rules, filepath.Join(absolute, ".rsch", "steering"), Repository, workspace)
	if err != nil {
		return Set{}, err
	}
	// Directory-scoped rules can be placed in nested .rsch/steering folders.
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != absolute && ignored(filepath.Base(path)) {
			return filepath.SkipDir
		}
		if entry.IsDir() && filepath.Base(path) == "steering" && filepath.Base(filepath.Dir(path)) == ".rsch" && filepath.Clean(path) != filepath.Join(absolute, ".rsch", "steering") {
			rules, err = appendRules(rules, path, Directory, workspace)
			if err != nil {
				return err
			}
			return filepath.SkipDir
		}
		if entry.IsDir() || filepath.Clean(path) == filepath.Join(absolute, ".rsch", "steering") {
			return nil
		}
		return nil
	})
	if err != nil {
		return Set{}, err
	}
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority == rules[j].Priority {
			return rules[i].Path < rules[j].Path
		}
		return rules[i].Priority < rules[j].Priority
	})
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, "# "+rule.Name+" ["+string(rule.Scope)+"]\n"+rule.Content)
	}
	return Set{Rules: rules, Combined: strings.Join(parts, "\n\n")}, nil
}

func appendRules(rules []Rule, dir string, defaultScope Scope, workspace *os.Root) ([]Rule, error) {
	var entries []os.DirEntry
	var err error
	readFile := os.ReadFile
	if workspace == nil {
		// Global and organization roots are explicitly outside the workspace.
		entries, err = os.ReadDir(dir)
	} else {
		relative, relErr := filepath.Rel(workspace.Name(), dir)
		if relErr != nil {
			return nil, relErr
		}
		directory, openErr := workspace.Open(relative)
		if os.IsNotExist(openErr) {
			return rules, nil
		}
		if openErr != nil {
			return nil, openErr
		}
		defer directory.Close()
		entries, err = directory.ReadDir(-1)
		readFile = func(path string) ([]byte, error) {
			relative, err := filepath.Rel(workspace.Name(), path)
			if err != nil {
				return nil, err
			}
			return workspace.ReadFile(relative)
		}
	}
	if os.IsNotExist(err) {
		return rules, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		content, err := readFile(path)
		if err != nil {
			return nil, err
		}
		scope := defaultScope
		lower := strings.ToLower(entry.Name())
		for candidate := range precedence {
			if strings.HasPrefix(lower, string(candidate)+".") || strings.HasPrefix(lower, string(candidate)+"-") {
				scope = candidate
				break
			}
		}
		rules = append(rules, Rule{Name: entry.Name(), Path: path, Scope: scope, Priority: precedence[scope], Content: string(content)})
	}
	return rules, nil
}

func ignored(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}
