// Package skill manages repository-scoped reusable skills.
package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const template = "# Skill\n\n## Purpose\nDescribe when this skill applies.\n\n## Instructions\nAdd reusable, repository-scoped guidance here.\n\n## Verification\nDocument how this skill is validated.\n"

const (
	manifestFile = "metadata.json"
	disabledFile = ".disabled"
)

// Manifest is the optional, machine-readable contract for a skill. SKILL.md
// remains the source of human-readable instructions; metadata is deliberately
// separate so discovery and routing never need to load every skill into model
// context.
type Manifest struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Description          string   `json:"description,omitempty"`
	Version              string   `json:"version,omitempty"`
	Author               string   `json:"author,omitempty"`
	License              string   `json:"license,omitempty"`
	Source               string   `json:"source,omitempty"`
	Category             string   `json:"category,omitempty"`
	Tags                 []string `json:"tags,omitempty"`
	Invocation           string   `json:"invocation,omitempty"`
	ModelInvocable       bool     `json:"modelInvocable,omitempty"`
	UserInvocable        bool     `json:"userInvocable,omitempty"`
	Dependencies         []string `json:"dependencies,omitempty"`
	Conflicts            []string `json:"conflicts,omitempty"`
	RequiredTools        []string `json:"requiredTools,omitempty"`
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty"`
	ContextBudget        int      `json:"contextBudget,omitempty"`
	SecurityLevel        string   `json:"securityLevel,omitempty"`
	PlatformSupport      []string `json:"platformSupport,omitempty"`
	Priority             int      `json:"priority,omitempty"`
}

type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Files      []string  `json:"files"`
	HasSkillMD bool      `json:"hasSkillMd"`
	Enabled    bool      `json:"enabled"`
	Manifest   *Manifest `json:"manifest,omitempty"`
}

type SearchResult struct {
	Entry  Entry    `json:"entry"`
	Score  int      `json:"score"`
	Reason []string `json:"reason,omitempty"`
}

type AuditIssue struct {
	Skill    string `json:"skill"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
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
		return Manager{}, errors.New("skill workspace is not a directory")
	}
	return Manager{Root: absolute}, nil
}

func (m Manager) directory(name string) (string, error) {
	name, err := normalizedName(name)
	if err != nil {
		return "", err
	}
	root, err := m.skillsRoot(false)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

func validateName(name string) error {
	_, err := normalizedName(name)
	return err
}

func normalizedName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.ContainsAny(name, `/\`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	return name, nil
}

// skillsRoot rejects symlinked catalog paths. Skill installation is a
// workspace-local operation and must not follow a project-controlled link
// into an unrelated directory.
func (m Manager) skillsRoot(create bool) (string, error) {
	dataRoot := filepath.Join(m.Root, ".rsch")
	if info, err := os.Lstat(dataRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("skill data directory cannot be a symlink")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("skill data path is not a directory: %s", dataRoot)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if create {
		if err := os.MkdirAll(dataRoot, 0o700); err != nil {
			return "", err
		}
	}

	root := filepath.Join(dataRoot, "skills")
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("skill directory cannot be a symlink")
		}
		if !info.IsDir() {
			return "", fmt.Errorf("skill path is not a directory: %s", root)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	} else if create {
		if err := os.Mkdir(root, 0o700); err != nil {
			return "", err
		}
	}
	return root, nil
}

func (m Manager) List() ([]Entry, error) {
	root, err := m.skillsRoot(false)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0)
	for _, item := range entries {
		if item.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("skill catalog contains unsupported symlink: %s", item.Name())
		}
		if !item.IsDir() {
			continue
		}
		entry, err := m.inspect(item.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (m Manager) inspect(name string) (Entry, error) {
	name, err := normalizedName(name)
	if err != nil {
		return Entry{}, err
	}
	dir, err := m.directory(name)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Entry{}, err
	}
	if !info.IsDir() {
		return Entry{}, fmt.Errorf("skill path is not a directory: %s", dir)
	}
	files := []string{}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill contains unsupported symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if rel != disabledFile {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	sort.Strings(files)
	hasSkill := false
	for _, file := range files {
		if file == "SKILL.md" {
			hasSkill = true
			break
		}
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return Entry{}, err
	}
	enabled := true
	if _, disabledErr := os.Stat(filepath.Join(dir, disabledFile)); disabledErr == nil {
		enabled = false
	} else if !os.IsNotExist(disabledErr) {
		return Entry{}, disabledErr
	}
	return Entry{Name: name, Path: dir, Files: files, HasSkillMD: hasSkill, Enabled: enabled, Manifest: manifest}, nil
}

func readManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", manifestFile, err)
	}
	return &manifest, nil
}

func (m Manager) Info(name string) (Entry, error) { return m.inspect(name) }

// Resolve returns a deterministic dependency-ordered skill set. Manifest
// dependencies use either "name" or "name@version"; the latter prevents a
// skill from silently composing with an incompatible installed version.
// Dependencies are resolved before the requested skill and shared
// dependencies are included only once.
func (m Manager) Resolve(names ...string) ([]Entry, error) {
	if len(names) == 0 {
		return nil, errors.New("at least one skill is required")
	}
	state := make(map[string]uint8)
	resolved := make([]Entry, 0, len(names))
	var visit func(string) error
	visit = func(spec string) error {
		name, constraint := splitDependency(spec)
		name, err := normalizedName(name)
		if err != nil {
			return err
		}
		switch state[name] {
		case 1:
			return fmt.Errorf("skill dependency cycle includes %q", name)
		case 2:
			if constraint != "" {
				entry, err := m.Info(name)
				if err != nil || entry.Manifest == nil || !versionMatches(entry.Manifest.Version, constraint) {
					return fmt.Errorf("skill %q does not satisfy version %q", name, constraint)
				}
			}
			return nil
		}
		entry, err := m.Info(name)
		if err != nil {
			return fmt.Errorf("skill dependency %q is not installed: %w", name, err)
		}
		if !entry.Enabled {
			return fmt.Errorf("skill dependency %q is disabled", name)
		}
		if !entry.HasSkillMD {
			return fmt.Errorf("skill dependency %q is missing SKILL.md", name)
		}
		if constraint != "" && (entry.Manifest == nil || !versionMatches(entry.Manifest.Version, constraint)) {
			version := ""
			if entry.Manifest != nil {
				version = entry.Manifest.Version
			}
			return fmt.Errorf("skill %q has version %q, does not satisfy %q", name, version, constraint)
		}
		state[name] = 1
		if entry.Manifest != nil {
			for _, dependency := range entry.Manifest.Dependencies {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		for _, selected := range resolved {
			if conflicts(entry, selected) || conflicts(selected, entry) {
				return fmt.Errorf("skills %q and %q conflict", entry.Name, selected.Name)
			}
		}
		state[name] = 2
		resolved = append(resolved, entry)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func splitDependency(spec string) (string, string) {
	spec = strings.TrimSpace(spec)
	if index := strings.LastIndex(spec, "@"); index > 0 {
		return strings.TrimSpace(spec[:index]), strings.TrimSpace(spec[index+1:])
	}
	return spec, ""
}

func conflicts(left, right Entry) bool {
	if left.Manifest == nil {
		return false
	}
	for _, conflict := range left.Manifest.Conflicts {
		name, constraint := splitDependency(conflict)
		if name != right.Name {
			continue
		}
		if constraint == "" || right.Manifest == nil || versionMatches(right.Manifest.Version, constraint) {
			return true
		}
	}
	return false
}

func versionMatches(actual, expected string) bool {
	actualParts, ok := versionParts(actual)
	if !ok {
		return actual == expected
	}
	expected = strings.TrimSpace(expected)
	operator := "="
	for _, candidate := range []string{"<=", ">=", "<", ">", "^", "~", "="} {
		if strings.HasPrefix(expected, candidate) {
			operator = candidate
			expected = strings.TrimSpace(strings.TrimPrefix(expected, candidate))
			break
		}
	}
	expectedParts, ok := versionParts(expected)
	if !ok {
		return false
	}
	comparison := compareVersions(actualParts, expectedParts)
	switch operator {
	case "=":
		return comparison == 0
	case "<":
		return comparison < 0
	case "<=":
		return comparison <= 0
	case ">":
		return comparison > 0
	case ">=":
		return comparison >= 0
	case "^":
		return comparison >= 0 && actualParts[0] == expectedParts[0]
	case "~":
		return comparison >= 0 && actualParts[0] == expectedParts[0] && actualParts[1] == expectedParts[1]
	default:
		return false
	}
}

func versionParts(version string) ([]int, bool) {
	version = strings.TrimSpace(strings.TrimPrefix(version, "v"))
	parts := strings.Split(version, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return nil, false
	}
	result := make([]int, 3)
	for index, part := range parts {
		value := strings.SplitN(part, "-", 2)[0]
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return nil, false
		}
		result[index] = parsed
	}
	return result, true
}

func compareVersions(left, right []int) int {
	for index := 0; index < 3; index++ {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

// Compose returns the dependency-ordered Markdown bodies for the requested
// skills. It is an inspection primitive; it does not execute anything in a
// skill's tools directory.
func (m Manager) Compose(names ...string) (string, error) {
	entries, err := m.Resolve(names...)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(entry.Path, "SKILL.md"))
		if err != nil {
			return "", err
		}
		parts = append(parts, strings.TrimSpace(string(data)))
	}
	return strings.Join(parts, "\n\n"), nil
}

// SetEnabled changes activation state using a small marker file. This keeps
// the state inspectable, atomic, and independent from instruction content.
func (m Manager) SetEnabled(name string, enabled bool) error {
	dir, err := m.directory(name)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(dir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return fmt.Errorf("skill %q is not installed", name)
		}
		return statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("skill path cannot be a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf("skill path is not a directory: %s", dir)
	}
	workspace, err := os.OpenRoot(m.Root)
	if err != nil {
		return err
	}
	defer workspace.Close()
	root, err := workspace.OpenRoot(filepath.Join(".rsch", "skills", filepath.Base(dir)))
	if err != nil {
		return err
	}
	defer root.Close()
	if enabled {
		if err := root.Remove(disabledFile); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// The marker's presence is sufficient. Never truncate an existing file or
	// follow a marker symlink, including one targeting the skill instructions.
	file, err := root.OpenFile(disabledFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		info, err := root.Lstat(disabledFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("skill disabled marker must be a regular file")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte("disabled\n")); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Search ranks metadata and bounded SKILL.md excerpts. It never returns the
// instruction body, which keeps search useful for routing without inflating
// model context.
func (m Manager) Search(query string, limit int) ([]SearchResult, error) {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(terms) == 0 {
		return nil, errors.New("skill search query is required")
	}
	entries, err := m.List()
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0)
	for _, entry := range entries {
		var haystack strings.Builder
		haystack.WriteString(strings.ToLower(entry.Name))
		if entry.Manifest != nil {
			data, _ := json.Marshal(entry.Manifest)
			haystack.WriteByte(' ')
			haystack.Write(data)
		}
		if data, readErr := readSearchExcerpt(filepath.Join(entry.Path, "SKILL.md")); readErr == nil {
			haystack.WriteByte(' ')
			haystack.Write(data)
		}
		text := strings.ToLower(haystack.String())
		score := 0
		reasons := make([]string, 0)
		for _, term := range terms {
			if strings.Contains(text, term) {
				score++
				if strings.Contains(strings.ToLower(entry.Name), term) {
					score += 3
					reasons = append(reasons, "name match")
				} else {
					reasons = append(reasons, "metadata or instruction match")
				}
			}
		}
		if score > 0 {
			results = append(results, SearchResult{Entry: entry, Score: score, Reason: unique(reasons)})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Entry.Name < results[j].Entry.Name
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func readSearchExcerpt(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, 128*1024))
}

func unique(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

// Audit validates every installed skill without executing scripts or
// interpreting instruction content. Findings are deterministic and suitable
// for CI or a human-readable doctor command.
func (m Manager) Audit() ([]AuditIssue, error) {
	root, err := m.skillsRoot(false)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []AuditIssue{}, nil
	}
	if err != nil {
		return nil, err
	}
	issues := make([]AuditIssue, 0)
	for _, item := range items {
		if !item.IsDir() {
			issues = append(issues, AuditIssue{Skill: item.Name(), Severity: "warning", Message: "skill catalog entry is not a directory"})
			continue
		}
		name := item.Name()
		entry, inspectErr := m.inspect(name)
		if inspectErr != nil {
			issues = append(issues, AuditIssue{Skill: name, Severity: "error", Message: inspectErr.Error()})
			continue
		}
		if !entry.HasSkillMD {
			issues = append(issues, AuditIssue{Skill: name, Severity: "error", Message: "SKILL.md is missing"})
		}
		if entry.Manifest != nil && strings.TrimSpace(entry.Manifest.ID) == "" {
			issues = append(issues, AuditIssue{Skill: name, Severity: "warning", Message: "metadata.json has no id"})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Skill != issues[j].Skill {
			return issues[i].Skill < issues[j].Skill
		}
		return issues[i].Message < issues[j].Message
	})
	return issues, nil
}

func (m Manager) Create(name string) (string, error) {
	dir, err := m.directory(name)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dir); err == nil {
		return "", fmt.Errorf("skill %q already exists", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if _, err := m.skillsRoot(true); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(template), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (m Manager) Remove(name string) error {
	dir, err := m.directory(name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return fmt.Errorf("skill %q is not installed", name)
	} else if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("skill path cannot be a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf("skill path is not a directory: %s", dir)
	}
	return os.RemoveAll(dir)
}

// Install copies a local skill source without overwriting an existing skill.
// A directory must contain SKILL.md; a single file is installed as SKILL.md.
func (m Manager) Install(name, source string) (string, error) {
	destination, err := m.directory(name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(source) == "" {
		return "", errors.New("skill install source is required")
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("skill %q already exists", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("skill install source cannot be a symlink")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", errors.New("skill install source must be a regular file or directory")
	}
	skillsRoot, err := m.skillsRoot(true)
	if err != nil {
		return "", err
	}
	items, err := collectSource(source, info)
	if err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(skillsRoot, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	for _, item := range items {
		target := filepath.Join(temporary, item.relative)
		if item.directory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if err := copyFile(item.source, target); err != nil {
			return "", err
		}
	}
	if info, err := os.Lstat(filepath.Join(temporary, "SKILL.md")); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("skill source must contain a regular SKILL.md file")
	}
	if _, err := readManifest(temporary); err != nil {
		return "", fmt.Errorf("invalid skill source: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("skill %q already exists", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", err
	}
	return destination, nil
}

type sourceItem struct {
	source    string
	relative  string
	directory bool
}

func collectSource(source string, info os.FileInfo) ([]sourceItem, error) {
	if !info.IsDir() {
		return []sourceItem{{source: source, relative: "SKILL.md"}}, nil
	}
	items := make([]sourceItem, 0)
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill source contains unsupported symlink: %s", path)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			items = append(items, sourceItem{source: path, relative: rel, directory: true})
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("skill source contains unsupported file: %s", path)
		}
		items = append(items, sourceItem{source: path, relative: rel})
		return nil
	})
	return items, err
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
