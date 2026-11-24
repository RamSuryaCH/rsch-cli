package intelligence

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type GraphNode struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}
type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}
type Graph struct {
	Version   int         `json:"version"`
	Root      string      `json:"root"`
	Generated time.Time   `json:"generatedAt"`
	Nodes     []GraphNode `json:"nodes"`
	Edges     []GraphEdge `json:"edges"`
}
type Impact struct {
	Changed          []string `json:"changed"`
	DirectFiles      []string `json:"directFiles"`
	IndirectFiles    []string `json:"indirectFiles"`
	Tests            []string `json:"tests"`
	APIs             []string `json:"apis"`
	DatabaseEntities []string `json:"databaseEntities"`
	Risk             string   `json:"risk"`
}

func (i *Index) Architecture(ctx context.Context) (Graph, error) {
	if i == nil {
		return Graph{}, errors.New("repository index is required")
	}
	if len(i.Files()) == 0 {
		if err := i.Build(ctx); err != nil {
			return Graph{}, err
		}
	}
	files := []File{}
	resolvedPaths := make(map[string]string)
	ignoreRules := newIgnoreMatcher(i.root)
	for _, file := range i.Files() {
		if err := ctx.Err(); err != nil {
			return Graph{}, err
		}
		resolved, allowed, err := resolveIndexedPath(i.root, filepath.Join(i.root, filepath.FromSlash(file.Path)), ignoreRules)
		if err != nil || !allowed {
			continue
		}
		files = append(files, file)
		resolvedPaths[file.Path] = resolved
	}
	symbolsByFile := make(map[string][]Symbol)
	for _, symbol := range i.Symbols() {
		symbolsByFile[symbol.File] = append(symbolsByFile[symbol.File], symbol)
	}
	imports := newLocalImportResolver(i.root, files)
	graph := Graph{Version: 1, Root: i.root, Generated: time.Now().UTC(), Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	for _, file := range files {
		id := "file:" + file.Path
		kind := "file"
		lower := strings.ToLower(file.Path)
		if strings.HasSuffix(lower, "_test.go") || strings.Contains(lower, ".test.") || strings.Contains(lower, "/tests/") {
			kind = "test"
		}
		if strings.Contains(lower, "/api/") || strings.Contains(lower, "handler") || strings.Contains(lower, "route") {
			kind = "api"
		}
		if strings.Contains(lower, "migration") || strings.Contains(lower, "schema") || strings.HasSuffix(lower, ".sql") {
			kind = "database"
		}
		graph.Nodes = append(graph.Nodes, GraphNode{ID: id, Kind: kind, Name: filepath.Base(file.Path), Path: file.Path})
		for _, symbol := range symbolsByFile[file.Path] {
			graph.Nodes = append(graph.Nodes, GraphNode{ID: "symbol:" + file.Path + ":" + symbol.Name, Kind: "symbol", Name: symbol.Name, Path: file.Path})
			graph.Edges = append(graph.Edges, GraphEdge{From: id, To: "symbol:" + file.Path + ":" + symbol.Name, Kind: "defines"})
		}
		if err := ctx.Err(); err != nil {
			return Graph{}, err
		}
		data, err := os.ReadFile(resolvedPaths[file.Path])
		if err != nil {
			continue
		}
		for _, target := range imports.targets(file.Path, data) {
			graph.Edges = append(graph.Edges, GraphEdge{From: id, To: "file:" + target, Kind: "imports"})
		}
	}
	sort.Slice(graph.Nodes, func(a, b int) bool { return graph.Nodes[a].ID < graph.Nodes[b].ID })
	sort.Slice(graph.Edges, func(a, b int) bool {
		if graph.Edges[a].From == graph.Edges[b].From {
			return graph.Edges[a].To < graph.Edges[b].To
		}
		return graph.Edges[a].From < graph.Edges[b].From
	})
	return graph, nil
}

type localImportResolver struct {
	modulePath string
	paths      map[string]bool
	stems      map[string][]string
	goByDir    map[string][]string
}

func newLocalImportResolver(root string, files []File) localImportResolver {
	resolver := localImportResolver{
		modulePath: readModulePath(root),
		paths:      make(map[string]bool, len(files)),
		stems:      make(map[string][]string, len(files)),
		goByDir:    make(map[string][]string),
	}
	for _, file := range files {
		filePath := path.Clean(filepath.ToSlash(file.Path))
		resolver.paths[filePath] = true
		stem := strings.TrimSuffix(filePath, path.Ext(filePath))
		resolver.stems[stem] = append(resolver.stems[stem], filePath)
		if strings.EqualFold(path.Ext(filePath), ".go") && !strings.HasSuffix(filePath, "_test.go") {
			directory := path.Dir(filePath)
			if directory == "." {
				directory = ""
			}
			resolver.goByDir[directory] = append(resolver.goByDir[directory], filePath)
		}
	}
	for key := range resolver.stems {
		sort.Strings(resolver.stems[key])
	}
	for key := range resolver.goByDir {
		sort.Strings(resolver.goByDir[key])
	}
	return resolver
}

func readModulePath(root string) string {
	resolved, allowed, err := resolveIndexedPath(root, filepath.Join(root, "go.mod"), newIgnoreMatcher(root))
	if err != nil || !allowed {
		return ""
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.TrimSpace(fields[1])
		}
	}
	return ""
}

func (r localImportResolver) targets(source string, data []byte) []string {
	specifiers := importSpecifiers(source, data)
	seen := make(map[string]bool)
	targets := make([]string, 0)
	add := func(target string) {
		target = path.Clean(filepath.ToSlash(target))
		if target == source || !r.paths[target] || seen[target] {
			return
		}
		seen[target] = true
		targets = append(targets, target)
	}
	for _, specifier := range specifiers {
		for _, target := range r.resolve(source, specifier) {
			add(target)
		}
	}
	sort.Strings(targets)
	return targets
}

func (r localImportResolver) resolve(source, specifier string) []string {
	specifier = strings.TrimSpace(specifier)
	if specifier == "" {
		return nil
	}
	if r.modulePath != "" && (specifier == r.modulePath || strings.HasPrefix(specifier, r.modulePath+"/")) {
		directory := strings.TrimPrefix(strings.TrimPrefix(specifier, r.modulePath), "/")
		return append([]string(nil), r.goByDir[directory]...)
	}

	sourceDir := path.Dir(filepath.ToSlash(source))
	if sourceDir == "." {
		sourceDir = ""
	}
	if strings.HasPrefix(specifier, ".") {
		candidate := path.Clean(path.Join(sourceDir, specifier))
		return r.resolveStem(candidate)
	}

	// Python imports use dotted module paths; bare imports are also useful for
	// small repositories and legacy Go fixtures without a module declaration.
	candidate := strings.ReplaceAll(specifier, ".", "/")
	if matches := r.resolveStem(candidate); len(matches) > 0 {
		return matches
	}
	return r.resolveStem(path.Join(sourceDir, candidate))
}

func (r localImportResolver) resolveStem(candidate string) []string {
	candidate = strings.TrimPrefix(path.Clean(filepath.ToSlash(candidate)), "./")
	if r.paths[candidate] {
		return []string{candidate}
	}
	resolved := append([]string(nil), r.stems[candidate]...)
	resolved = append(resolved, r.stems[path.Join(candidate, "index")]...)
	resolved = append(resolved, r.stems[path.Join(candidate, "__init__")]...)
	return resolved
}

var (
	quotedImportPattern = regexp.MustCompile("(?m)(?:\\bfrom\\s+|\\brequire\\s*\\(\\s*|\\bimport\\s*\\(\\s*|^\\s*import\\s+)[\"'`]([^\"'`]+)[\"'`]")
	pythonFromPattern   = regexp.MustCompile(`(?m)^\s*from\s+([.A-Za-z_][.A-Za-z0-9_]*)\s+import\s+`)
	pythonImportPattern = regexp.MustCompile(`(?m)^\s*import\s+([A-Za-z_][.A-Za-z0-9_]*)`)
)

func importSpecifiers(source string, data []byte) []string {
	if strings.EqualFold(path.Ext(source), ".go") {
		parsed, err := parser.ParseFile(token.NewFileSet(), source, data, parser.ImportsOnly)
		if err == nil {
			imports := make([]string, 0, len(parsed.Imports))
			for _, declaration := range parsed.Imports {
				value, unquoteErr := strconv.Unquote(declaration.Path.Value)
				if unquoteErr == nil {
					imports = append(imports, value)
				}
			}
			return imports
		}
	}

	text := string(data)
	specifiers := make([]string, 0)
	for _, match := range quotedImportPattern.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			specifiers = append(specifiers, match[1])
		}
	}
	if strings.EqualFold(path.Ext(source), ".py") {
		for _, pattern := range []*regexp.Regexp{pythonFromPattern, pythonImportPattern} {
			for _, match := range pattern.FindAllStringSubmatch(text, -1) {
				if len(match) > 1 {
					specifiers = append(specifiers, pythonImportPath(source, match[1]))
				}
			}
		}
	}
	return specifiers
}

func pythonImportPath(source, specifier string) string {
	if !strings.HasPrefix(specifier, ".") {
		return strings.ReplaceAll(specifier, ".", "/")
	}
	leading := len(specifier) - len(strings.TrimLeft(specifier, "."))
	directory := path.Dir(filepath.ToSlash(source))
	for level := 1; level < leading; level++ {
		directory = path.Dir(directory)
	}
	remainder := strings.ReplaceAll(strings.TrimLeft(specifier, "."), ".", "/")
	return path.Join(directory, remainder)
}

func (i *Index) Impact(ctx context.Context, changed []string) (Impact, error) {
	graph, err := i.Architecture(ctx)
	if err != nil {
		return Impact{}, err
	}
	changedSet := map[string]bool{}
	for _, raw := range changed {
		path := filepath.ToSlash(strings.TrimSpace(raw))
		if path != "" {
			changedSet["file:"+path] = true
		}
	}
	impact := Impact{Changed: append([]string{}, changed...), DirectFiles: []string{}, IndirectFiles: []string{}, Tests: []string{}, APIs: []string{}, DatabaseEntities: []string{}, Risk: "low"}
	nodes := map[string]GraphNode{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
		if changedSet[node.ID] && node.Kind == "file" || changedSet[node.ID] && node.Kind != "symbol" {
			impact.DirectFiles = append(impact.DirectFiles, node.Path)
		}
	}
	reverse := map[string][]string{}
	for _, edge := range graph.Edges {
		if edge.Kind == "imports" {
			reverse[edge.To] = append(reverse[edge.To], edge.From)
		}
	}
	seen := map[string]bool{}
	queue := []string{}
	for id := range changedSet {
		seen[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, parent := range reverse[current] {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			queue = append(queue, parent)
			if node, ok := nodes[parent]; ok && node.Path != "" {
				impact.IndirectFiles = append(impact.IndirectFiles, node.Path)
			}
		}
	}
	for _, path := range impact.DirectFiles {
		node := nodeByPath(nodes, path)
		classifyImpact(&impact, node)
	}
	for _, path := range impact.IndirectFiles {
		node := nodeByPath(nodes, path)
		classifyImpact(&impact, node)
	}
	sort.Strings(impact.DirectFiles)
	sort.Strings(impact.IndirectFiles)
	sort.Strings(impact.Tests)
	sort.Strings(impact.APIs)
	sort.Strings(impact.DatabaseEntities)
	if len(impact.APIs) > 0 || len(impact.DatabaseEntities) > 0 || hasHighRiskPath(impact.DirectFiles) || len(impact.IndirectFiles) > 20 {
		impact.Risk = "high"
	} else if len(impact.IndirectFiles) > 5 {
		impact.Risk = "medium"
	}
	return impact, nil
}

func nodeByPath(nodes map[string]GraphNode, path string) GraphNode {
	for _, node := range nodes {
		if node.Path == path && node.Kind != "symbol" {
			return node
		}
	}
	return GraphNode{}
}
func classifyImpact(impact *Impact, node GraphNode) {
	if node.Path == "" {
		return
	}
	switch node.Kind {
	case "test":
		appendString(&impact.Tests, node.Path)
	case "api":
		appendString(&impact.APIs, node.Path)
	case "database":
		appendString(&impact.DatabaseEntities, node.Path)
	}
}
func appendString(items *[]string, value string) {
	for _, item := range *items {
		if item == value {
			return
		}
	}
	*items = append(*items, value)
}
func hasHighRiskPath(paths []string) bool {
	for _, path := range paths {
		lower := strings.ToLower(path)
		if strings.Contains(lower, "auth") || strings.Contains(lower, "security") || strings.Contains(lower, "migration") {
			return true
		}
	}
	return false
}

func SaveArchitecture(root string, graph Graph) error {
	data, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, ".rsch", "intelligence", "architecture.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
