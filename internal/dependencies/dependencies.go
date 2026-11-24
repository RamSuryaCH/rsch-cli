// Package dependencies extracts dependency facts without requiring a model.
package dependencies

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/process"
)

type Dependency struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
	Manager    string `json:"manager"`
	Direct     bool   `json:"direct"`
	SourceFile string `json:"sourceFile"`
	Scope      string `json:"scope,omitempty"`
}

type Report struct {
	Root              string          `json:"root"`
	AffectedAvailable bool            `json:"affectedAvailable"`
	Packages          []Package       `json:"packages,omitempty"`
	ExecutionPlans    []ExecutionPlan `json:"executionPlans"`
	Dependencies      []Dependency    `json:"dependencies"`
	Findings          []Finding       `json:"findings,omitempty"`
	Warnings          []string        `json:"warnings,omitempty"`
}

// Package identifies an independently declared workspace package. It makes
// monorepo scope explicit instead of pretending that only the repository root
// has dependencies.
type Package struct {
	Path          string   `json:"path"`
	Manager       string   `json:"manager"`
	Manifest      string   `json:"manifest"`
	Affected      bool     `json:"affected"`
	AffectedFiles []string `json:"affectedFiles,omitempty"`
}

// ExecutionPlan describes bounded verification commands for an affected
// package. Plans are advisory data: Scan never executes them.
type ExecutionPlan struct {
	PackagePath string     `json:"packagePath"`
	Directory   string     `json:"directory"`
	Manager     string     `json:"manager"`
	Commands    [][]string `json:"commands"`
	Available   bool       `json:"available"`
	Reason      string     `json:"reason,omitempty"`
}

// Finding is evidence-backed when Tool is set. Findings never imply that a
// package is vulnerable merely because an audit tool is unavailable.
type Finding struct {
	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Current   string `json:"current,omitempty"`
	Available string `json:"available,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
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
		return Report{}, errors.New("dependency root is not a directory")
	}
	report := Report{Root: absolute, Packages: []Package{}, ExecutionPlans: []ExecutionPlan{}, Dependencies: []Dependency{}, Warnings: []string{}}
	packages, err := discoverPackages(ctx, absolute)
	if err != nil {
		return Report{}, err
	}
	report.Packages = packages
	changed, available, changeErr := changedFiles(ctx, absolute)
	report.AffectedAvailable = available
	if changeErr != nil {
		report.Warnings = append(report.Warnings, "affected package detection unavailable: "+changeErr.Error())
	} else if available {
		markAffected(report.Packages, changed)
		report.ExecutionPlans = BuildExecutionPlans(absolute, report.Packages)
	}
	for _, packageInfo := range packages {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		packageRoot := filepath.Dir(filepath.Join(absolute, filepath.FromSlash(packageInfo.Manifest)))
		var items []Dependency
		var warning string
		switch packageInfo.Manager {
		case "go":
			items, warning = scanGo(ctx, packageRoot)
		case "npm":
			items, err = scanPackageJSON(packageRoot)
		case "pip":
			items, err = scanRequirements(filepath.Join(packageRoot, filepath.Base(packageInfo.Manifest)))
		}
		if err != nil {
			return Report{}, err
		}
		prefixDependencySources(items, filepath.ToSlash(filepath.Dir(filepath.FromSlash(packageInfo.Manifest))))
		report.Dependencies = append(report.Dependencies, items...)
		if warning != "" {
			report.Warnings = append(report.Warnings, packageInfo.Manifest+": "+warning)
		}
		switch packageInfo.Manager {
		case "go":
			if updateErr := enrichGoUpdates(ctx, packageRoot, &report); updateErr != nil {
				report.Warnings = append(report.Warnings, updateErr.Error())
			}
			if auditErr := enrichAudits(ctx, packageRoot, &report); auditErr != nil {
				report.Warnings = append(report.Warnings, auditErr.Error())
			}
		case "npm":
			if updateErr := enrichNPM(ctx, packageRoot, &report); updateErr != nil {
				report.Warnings = append(report.Warnings, updateErr.Error())
			}
		}
	}
	sort.Slice(report.Dependencies, func(i, j int) bool {
		if report.Dependencies[i].Manager == report.Dependencies[j].Manager {
			return report.Dependencies[i].Name < report.Dependencies[j].Name
		}
		return report.Dependencies[i].Manager < report.Dependencies[j].Manager
	})
	report.Findings = append(report.Findings, analyzeDuplicates(report.Dependencies)...)
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}

// BuildExecutionPlans creates verification plans for affected packages. It
// checks only bounded local tool availability and manifest metadata; it never
// invokes a package manager, test runner, or network command.
func BuildExecutionPlans(root string, packages []Package) []ExecutionPlan {
	plans := make([]ExecutionPlan, 0)
	for _, packageInfo := range packages {
		if !packageInfo.Affected {
			continue
		}
		directory := filepath.Join(root, packageInfo.Path)
		if packageInfo.Path == "" || packageInfo.Path == "." {
			directory = root
		}
		plan := ExecutionPlan{
			PackagePath: packageInfo.Path,
			Directory:   directory,
			Manager:     packageInfo.Manager,
			Commands:    [][]string{},
		}
		switch packageInfo.Manager {
		case "go":
			plan.Commands = [][]string{{"go", "test", "./..."}, {"go", "vet", "./..."}}
			plan.Available = process.Exists("go")
			if plan.Available {
				plan.Reason = "go executable available; commands are planned but not executed"
			} else {
				plan.Reason = "go executable is unavailable"
			}
		case "npm":
			plan.Commands = [][]string{{"npm", "test"}}
			plan.Available = process.Exists("npm")
			if plan.Available {
				plan.Reason = "npm executable available; package.json test script will be resolved at execution time"
			} else {
				plan.Reason = "npm executable is unavailable"
			}
		case "pip":
			plan.Commands = [][]string{{"python", "-m", "pytest"}}
			plan.Available = process.Exists("python")
			if plan.Available {
				plan.Reason = "python executable available; pytest module availability is not probed during planning"
			} else {
				plan.Reason = "python executable is unavailable; pytest cannot be planned for execution"
			}
		default:
			plan.Reason = "no bounded verification commands are defined for this package manager"
		}
		plans = append(plans, plan)
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].PackagePath == plans[j].PackagePath {
			return plans[i].Manager < plans[j].Manager
		}
		return plans[i].PackagePath < plans[j].PackagePath
	})
	return plans
}

// changedFiles returns paths reported by Git relative to root. It is kept
// separate from package discovery so callers can test affected-package
// mapping without requiring a repository or a process boundary.
func changedFiles(ctx context.Context, root string) ([]string, bool, error) {
	if !process.Exists("git") {
		return nil, false, errors.New("git is not installed")
	}
	prefixOutput, err := runJSON(ctx, root, "git", "--no-optional-locks", "rev-parse", "--show-prefix")
	if err != nil {
		return nil, false, err
	}
	prefix := strings.TrimSuffix(string(prefixOutput), "\n")
	output, err := runJSON(ctx, root, "git", "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ".")
	if err != nil {
		return nil, false, err
	}
	paths := make([]string, 0)
	for _, path := range parseChangedFiles(string(output)) {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		path = strings.TrimPrefix(path, prefix)
		if path != ".rsch" && !strings.HasPrefix(path, ".rsch/") {
			paths = append(paths, path)
		}
	}
	return paths, true, nil
}

func parseChangedFiles(output string) []string {
	seen := map[string]bool{}
	paths := make([]string, 0)
	records := strings.Split(output, "\x00")
	for i := 0; i < len(records); i++ {
		line := records[i]
		if len(line) < 4 {
			continue
		}
		// Porcelain -z emits literal paths, with a second NUL-delimited
		// source path for renames/copies. Whitespace and arrows can be part
		// of an ordinary filename and must not be interpreted as syntax.
		candidates := []string{line[3:]}
		if strings.ContainsAny(line[:2], "RC") && i+1 < len(records) {
			i++
			candidates = append(candidates, records[i])
		}
		for _, candidate := range candidates {
			candidate = filepath.ToSlash(candidate)
			candidate = strings.TrimPrefix(candidate, "./")
			if candidate != "" && !seen[candidate] && !strings.HasPrefix(candidate, ".rsch/") {
				seen[candidate] = true
				paths = append(paths, candidate)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

// markAffected marks packages whose directory contains a changed path. A
// root package (.) intentionally owns all repository changes; nested packages
// only own changes beneath their own directory or their manifest.
func markAffected(packages []Package, changed []string) {
	for i := range packages {
		packageRoot := filepath.ToSlash(filepath.Clean(packages[i].Path))
		if packageRoot == "." || packageRoot == "" {
			packageRoot = ""
		}
		for _, path := range changed {
			if packageRoot == "" || path == filepath.ToSlash(packages[i].Manifest) || strings.HasPrefix(path, packageRoot+"/") {
				packages[i].Affected = true
				packages[i].AffectedFiles = append(packages[i].AffectedFiles, path)
			}
		}
		sort.Strings(packages[i].AffectedFiles)
	}
}

func discoverPackages(ctx context.Context, root string) ([]Package, error) {
	packages := []Package{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && ignored(filepath.Base(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		manager := ""
		switch strings.ToLower(filepath.Base(path)) {
		case "go.mod":
			manager = "go"
		case "package.json":
			manager = "npm"
		case "requirements.txt", "requirements-dev.txt":
			manager = "pip"
		}
		if manager == "" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		packages = append(packages, Package{Path: filepath.ToSlash(filepath.Dir(relative)), Manager: manager, Manifest: filepath.ToSlash(relative)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Manifest < packages[j].Manifest })
	return packages, nil
}

func prefixDependencySources(items []Dependency, directory string) {
	directory = strings.Trim(directory, "/.")
	if directory == "" {
		return
	}
	for index := range items {
		items[index].SourceFile = filepath.ToSlash(filepath.Join(directory, items[index].SourceFile))
	}
}

func ignored(name string) bool {
	switch name {
	case ".git", ".rsch", "node_modules", "vendor", "dist", "build", "target":
		return true
	default:
		return false
	}
}

func analyzeDuplicates(items []Dependency) []Finding {
	seen := map[string][]Dependency{}
	for _, item := range items {
		seen[item.Manager+":"+strings.ToLower(item.Name)] = append(seen[item.Manager+":"+strings.ToLower(item.Name)], item)
	}
	findings := []Finding{}
	for key, values := range seen {
		versions := map[string]bool{}
		for _, value := range values {
			versions[value.Version] = true
		}
		if len(values) > 1 || len(versions) > 1 {
			findings = append(findings, Finding{Kind: "duplicate-or-conflict", Name: key, Evidence: "dependency appears more than once or has conflicting declarations"})
		}
	}
	return findings
}

func runJSON(ctx context.Context, root, tool string, args ...string) ([]byte, error) {
	return runJSONAcceptExit(ctx, root, tool, 0, args...)
}

func runJSONAcceptExit(ctx context.Context, root, tool string, acceptedExitCode int, args ...string) ([]byte, error) {
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := process.RunOutput(check, process.Spec{Argv: append([]string{tool}, args...), Directory: root, MaxOutputBytes: 16 << 20})
	if check.Err() != nil {
		return nil, check.Err()
	}
	if err != nil && acceptedExitCode != 0 && result.ExitCode == acceptedExitCode && !result.OutputTruncated {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return result.Output, nil
}

func enrichGoUpdates(ctx context.Context, root string, report *Report) error {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return nil
	}
	data, err := runJSON(ctx, root, "go", "list", "-m", "-u", "-json", "all")
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var module struct {
			Path, Version string
			Update        *struct{ Version string }
		}
		if err := decoder.Decode(&module); err != nil {
			if err == io.EOF {
				break
			}
			return nil
		}
		if module.Update != nil && module.Update.Version != "" {
			report.Findings = append(report.Findings, Finding{Kind: "outdated", Name: module.Path, Current: module.Version, Available: module.Update.Version, Tool: "go list -m -u", Evidence: "module graph reported an available update"})
		}
	}
	return nil
}

func enrichNPM(ctx context.Context, root string, report *Report) error {
	if _, err := os.Stat(filepath.Join(root, "package.json")); err != nil {
		return nil
	}
	// npm outdated exits with status 1 when updates are found; its JSON
	// still contains the successful result in that case.
	data, err := runJSONAcceptExit(ctx, root, "npm", 1, "outdated", "--json")
	if err != nil {
		return nil
	}
	var values map[string]struct{ Current, Wanted, Latest string }
	if json.Unmarshal(data, &values) != nil {
		return nil
	}
	for name, value := range values {
		available := value.Latest
		if available == "" {
			available = value.Wanted
		}
		if available == "" {
			continue
		}
		report.Findings = append(report.Findings, Finding{Kind: "outdated", Name: name, Current: value.Current, Available: available, Tool: "npm outdated", Evidence: "npm reported an available update"})
	}
	return nil
}

func enrichAudits(ctx context.Context, root string, report *Report) error {
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		if process.Exists("govulncheck") {
			if data, runErr := runJSON(ctx, root, "govulncheck", "-json", "./..."); runErr == nil && len(data) > 0 {
				report.Findings = append(report.Findings, Finding{Kind: "security-audit", Tool: "govulncheck", Evidence: "govulncheck produced JSON output; inspect tool output for affected symbols"})
			}
		}
	}
	return nil
}

func scanGo(ctx context.Context, root string) ([]Dependency, string) {
	data, err := runJSON(ctx, root, "go", "list", "-m", "-json", "all")
	if err != nil {
		return parseGoMod(root), "go list -m -json all unavailable; using go.mod declarations"
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	items := []Dependency{}
	for {
		var module struct {
			Path     string
			Version  string
			Main     bool
			Indirect bool
		}
		if err := decoder.Decode(&module); err != nil {
			if err == io.EOF {
				break
			}
			return parseGoMod(root), "unable to decode go module graph; using go.mod declarations"
		}
		if module.Main {
			continue
		}
		direct := !module.Indirect
		items = append(items, Dependency{Name: module.Path, Version: module.Version, Manager: "go", Direct: direct, SourceFile: "go.mod", Scope: "runtime"})
	}
	return items, ""
}

func parseGoMod(root string) []Dependency {
	path := filepath.Join(root, "go.mod")
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	items := []Dependency{}
	inBlock := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "require (" {
			inBlock = true
			continue
		}
		if inBlock && line == ")" {
			inBlock = false
			continue
		}
		if strings.HasPrefix(line, "require ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		} else if !inBlock {
			continue
		}
		parts := strings.Fields(strings.TrimSuffix(line, "// indirect"))
		if len(parts) >= 2 {
			items = append(items, Dependency{Name: parts[0], Version: parts[1], Manager: "go", Direct: !strings.Contains(line, "// indirect"), SourceFile: "go.mod", Scope: "runtime"})
		}
	}
	return items
}

func scanPackageJSON(root string) ([]Dependency, error) {
	path := filepath.Join(root, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
		Dev          map[string]string `json:"devDependencies"`
		Optional     map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	items := []Dependency{}
	add := func(values map[string]string, direct bool, scope string) {
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			items = append(items, Dependency{Name: name, Version: values[name], Manager: "npm", Direct: direct, SourceFile: "package.json", Scope: scope})
		}
	}
	add(pkg.Dependencies, true, "runtime")
	add(pkg.Dev, true, "development")
	add(pkg.Optional, true, "optional")
	return items, nil
}

func scanRequirements(path string) ([]Dependency, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	manager := "pip"
	source := filepath.Base(path)
	scope := "runtime"
	if strings.Contains(source, "dev") {
		scope = "development"
	}
	items := []Dependency{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		name, version := line, ""
		for _, operator := range []string{"==", ">=", "<=", "~=", ">", "<"} {
			if index := strings.Index(line, operator); index >= 0 {
				name, version = strings.TrimSpace(line[:index]), strings.TrimSpace(line[index:])
				break
			}
		}
		if name != "" {
			items = append(items, Dependency{Name: name, Version: version, Manager: manager, Direct: true, SourceFile: source, Scope: scope})
		}
	}
	return items, scanner.Err()
}
