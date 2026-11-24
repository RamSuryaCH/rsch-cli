// Package intelligence provides deterministic repository facts. It is
// intentionally model-independent: indexing and retrieval can be used by
// planners, agents, review, context display, and API clients alike.
package intelligence

import (
	"bufio"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/research"
	"github.com/opencode-ai/opencode/internal/steering"
)

type File struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Hash       string    `json:"hash"`
	Language   string    `json:"language,omitempty"`
}
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"`
}
type Match struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Score int    `json:"score"`
}
type Provenance struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
	Hash   string `json:"hash"`
}
type Source struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
	Count  int    `json:"count,omitempty"`
}
type Context struct {
	Query       string                  `json:"query"`
	Matches     []Match                 `json:"matches"`
	Provenance  []Provenance            `json:"provenance"`
	Budget      int                     `json:"budget"`
	Used        int                     `json:"used"`
	Selected    []string                `json:"selected,omitempty"`
	Excluded    []string                `json:"excluded,omitempty"`
	Symbols     []Symbol                `json:"symbols,omitempty"`
	Sources     []Source                `json:"sources,omitempty"`
	Memory      []memory.Entry          `json:"memory,omitempty"`
	Claims      []research.SearchResult `json:"claims,omitempty"`
	GeneratedAt time.Time               `json:"generatedAt"`
}

type Index struct {
	root     string
	mu       sync.RWMutex
	files    map[string]File
	symbols  []Symbol
	cache    CacheMetadata
	readFile func(string) ([]byte, error)
}

const (
	indexCacheVersion = 4
	indexGenerator    = "rsch.repository-index"
	indexCacheScope   = "repository"
	indexSecurity     = "local-sensitive"
	maxIndexedBytes   = 2 * 1024 * 1024
)

// CacheMetadata makes cache scope, provenance, invalidation inputs, and local
// data classification explicit to callers and diagnostics.
type CacheMetadata struct {
	Version     int       `json:"version"`
	Generator   string    `json:"generator"`
	Scope       string    `json:"scope"`
	Security    string    `json:"security"`
	Root        string    `json:"root"`
	PolicyHash  string    `json:"policyHash"`
	BuiltAt     time.Time `json:"builtAt"`
	ValidatedAt time.Time `json:"validatedAt"`
}

type cache struct {
	Metadata CacheMetadata `json:"metadata"`
	Files    []File        `json:"files"`
	Symbols  []Symbol      `json:"symbols"`
}

func New(root string) (*Index, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("repository root is not a directory")
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	return &Index{root: absolute, files: map[string]File{}, cache: CacheMetadata{Version: indexCacheVersion, Generator: indexGenerator, Scope: indexCacheScope, Security: indexSecurity, Root: absolute, PolicyHash: indexPolicyHash()}, readFile: os.ReadFile}, nil
}

func (i *Index) Root() string { return i.root }
func (i *Index) Build(ctx context.Context) error {
	files := make(map[string]File)
	var symbols []Symbol
	previousFiles, previousSymbols, _ := loadCache(i.root)
	symbolsByFile := make(map[string][]Symbol)
	for _, symbol := range previousSymbols {
		symbolsByFile[symbol.File] = append(symbolsByFile[symbol.File], symbol)
	}
	ignoreRules := newIgnoreMatcher(i.root)
	err := filepath.WalkDir(i.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(i.root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if path != i.root && (ignored(filepath.Base(path)) || ignoreRules.ignored(relative, true)) {
				return fs.SkipDir
			}
			return nil
		}
		if ignored(filepath.Base(path)) || isSecretPath(relative) || ignoreRules.ignored(relative, false) || !isIndexable(path) {
			return nil
		}
		resolved, allowed, err := resolveIndexedPath(i.root, path, ignoreRules)
		if err != nil {
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			return err
		}
		if !allowed {
			return nil
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxIndexedBytes {
			return nil
		}
		if previous, ok := previousFiles[relative]; ok && previous.Size == info.Size() && previous.ModifiedAt.Equal(info.ModTime().UTC()) && previous.Hash != "" {
			files[relative] = previous
			symbols = append(symbols, symbolsByFile[relative]...)
			return nil
		}
		readFile := i.readFile
		if readFile == nil {
			readFile = os.ReadFile
		}
		data, err := readFile(resolved)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		file := File{Path: relative, Size: info.Size(), ModifiedAt: info.ModTime().UTC(), Hash: hex.EncodeToString(hash[:]), Language: language(path)}
		files[relative] = file
		symbols = append(symbols, extractSymbols(relative, data)...)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(symbols, func(a, b int) bool {
		if symbols[a].File == symbols[b].File {
			if symbols[a].Line == symbols[b].Line {
				if symbols[a].Name == symbols[b].Name {
					return symbols[a].Kind < symbols[b].Kind
				}
				return symbols[a].Name < symbols[b].Name
			}
			return symbols[a].Line < symbols[b].Line
		}
		return symbols[a].File < symbols[b].File
	})
	i.mu.Lock()
	i.files, i.symbols = files, symbols
	i.cache = CacheMetadata{Version: indexCacheVersion, Generator: indexGenerator, Scope: indexCacheScope, Security: indexSecurity, Root: i.root, PolicyHash: indexPolicyHash(), BuiltAt: time.Now().UTC(), ValidatedAt: time.Now().UTC()}
	i.mu.Unlock()
	if err := saveCache(i.root, files, symbols); err != nil {
		return err
	}
	return nil
}

func cachePath(root string) string { return filepath.Join(root, ".rsch", "intelligence", "index.json") }

func loadCache(root string) (map[string]File, []Symbol, CacheMetadata) {
	data, err := os.ReadFile(cachePath(root))
	if err != nil {
		return map[string]File{}, nil, CacheMetadata{}
	}
	var stored cache
	if json.Unmarshal(data, &stored) != nil || !validCacheMetadata(stored.Metadata, root) {
		return map[string]File{}, nil, CacheMetadata{}
	}
	files := make(map[string]File, len(stored.Files))
	for _, file := range stored.Files {
		files[file.Path] = file
	}
	stored.Metadata.ValidatedAt = time.Now().UTC()
	return files, stored.Symbols, stored.Metadata
}

func saveCache(root string, files map[string]File, symbols []Symbol) error {
	items := make([]File, 0, len(files))
	for _, file := range files {
		items = append(items, file)
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Path < items[b].Path })
	now := time.Now().UTC()
	data, err := json.MarshalIndent(cache{Metadata: CacheMetadata{Version: indexCacheVersion, Generator: indexGenerator, Scope: indexCacheScope, Security: indexSecurity, Root: root, PolicyHash: indexPolicyHash(), BuiltAt: now, ValidatedAt: now}, Files: items, Symbols: symbols}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(cachePath(root))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp := cachePath(root) + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, cachePath(root)); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func validCacheMetadata(metadata CacheMetadata, root string) bool {
	return metadata.Version == indexCacheVersion && metadata.Generator == indexGenerator && metadata.Scope == indexCacheScope && metadata.Security == indexSecurity && metadata.Root == root && metadata.PolicyHash == indexPolicyHash() && !metadata.BuiltAt.IsZero()
}

func indexPolicyHash() string {
	policy := strings.Join([]string{indexGenerator, "symbols=go-ast+language-patterns-v1", "ignored=.git,.rsch,.ssh,.aws,.azure,.gnupg,node_modules,vendor,dist,build,target+gitignore", "secrets=.env*,credentials,private-keys", "symlinks=workspace-contained+target-policy", "maxBytes=2097152", "extensions=go,ts,tsx,js,jsx,py,rs,java,kt,c,h,cpp,cc,cs,rb,php,swift,md,json,yaml,yml,toml,sql,ini,cfg,xml"}, "|")
	sum := sha256.Sum256([]byte(policy))
	return hex.EncodeToString(sum[:])
}

// CacheMetadata reports the last successfully persisted index metadata.
func (i *Index) CacheMetadata() CacheMetadata {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.cache
}

func (i *Index) Files() []File {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]File, 0, len(i.files))
	for _, file := range i.files {
		out = append(out, file)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}
func (i *Index) Symbols() []Symbol {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]Symbol(nil), i.symbols...)
}

func (i *Index) Search(ctx context.Context, query string, limit int) (Context, error) {
	if strings.TrimSpace(query) == "" {
		return Context{}, errors.New("search query is required")
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	terms := strings.Fields(strings.ToLower(query))
	candidates := make(rankedMatchHeap, 0, limit)
	heap.Init(&candidates)
	ignoreRules := newIgnoreMatcher(i.root)
	for _, file := range i.Files() {
		if err := ctx.Err(); err != nil {
			return Context{}, err
		}
		if isSecretPath(file.Path) || ignoreRules.ignored(file.Path, false) {
			continue
		}
		path := filepath.Join(i.root, filepath.FromSlash(file.Path))
		resolved, allowed, err := resolveIndexedPath(i.root, path, ignoreRules)
		if err != nil || !allowed {
			continue
		}
		handle, err := os.Open(resolved)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(handle)
		scanner.Buffer(make([]byte, 64*1024), maxIndexedBytes+1)
		line := 0
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				handle.Close()
				return Context{}, err
			}
			line++
			text := scanner.Text()
			lower := strings.ToLower(text)
			score := 0
			matchedTerms := 0
			for _, term := range terms {
				occurrences := strings.Count(lower, term)
				if occurrences > 0 {
					matchedTerms++
					if occurrences > 5 {
						occurrences = 5
					}
					score += occurrences
					if strings.Contains(strings.ToLower(file.Path), term) {
						score += 2
					}
				}
			}
			if len(terms) > 1 && matchedTerms == len(terms) {
				score += len(terms) + 2
			}
			phrase := strings.TrimSpace(strings.ToLower(query))
			if phrase != "" && strings.Contains(lower, phrase) {
				score += 3
			}
			if score > 0 {
				candidate := rankedMatch{
					Match:      Match{File: file.Path, Line: line, Text: truncate(text, 400), Score: score},
					Provenance: Provenance{File: file.Path, Line: line, Reason: "relevance-ranked term and phrase match for repository query", Hash: file.Hash},
				}
				if candidates.Len() < limit {
					heap.Push(&candidates, candidate)
				} else if matchBetter(candidate.Match, candidates[0].Match) {
					heap.Pop(&candidates)
					heap.Push(&candidates, candidate)
				}
			}
		}
		scanErr := scanner.Err()
		closeErr := handle.Close()
		if scanErr != nil {
			return Context{}, fmt.Errorf("scan %s: %w", file.Path, scanErr)
		}
		if closeErr != nil {
			return Context{}, fmt.Errorf("close %s: %w", file.Path, closeErr)
		}
	}
	ordered := make([]rankedMatch, candidates.Len())
	copy(ordered, candidates)
	sort.Slice(ordered, func(a, b int) bool { return matchBetter(ordered[a].Match, ordered[b].Match) })
	matches := make([]Match, 0, len(ordered))
	provenance := make([]Provenance, 0, len(ordered))
	for _, candidate := range ordered {
		matches = append(matches, candidate.Match)
		provenance = append(provenance, candidate.Provenance)
	}
	return Context{Query: query, Matches: matches, Provenance: provenance, GeneratedAt: time.Now().UTC()}, nil
}

type rankedMatch struct {
	Match      Match
	Provenance Provenance
}

// rankedMatchHeap keeps the least useful retained match at index zero, so a
// search only stores its best K candidates instead of accumulating every hit
// in a large repository.
type rankedMatchHeap []rankedMatch

func (h rankedMatchHeap) Len() int { return len(h) }
func (h rankedMatchHeap) Less(a, b int) bool {
	return matchWorse(h[a].Match, h[b].Match)
}
func (h rankedMatchHeap) Swap(a, b int) { h[a], h[b] = h[b], h[a] }
func (h *rankedMatchHeap) Push(value any) {
	*h = append(*h, value.(rankedMatch))
}
func (h *rankedMatchHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = rankedMatch{}
	*h = old[:last]
	return value
}

func matchBetter(a, b Match) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Line < b.Line
}

func matchWorse(a, b Match) bool {
	if a.Score != b.Score {
		return a.Score < b.Score
	}
	if a.File != b.File {
		return a.File > b.File
	}
	return a.Line > b.Line
}

// BuildContext selects bounded, deduplicated repository context. The score is
// deterministic: direct term matches outrank symbol/name matches, and files
// are admitted in stable order until the approximate token budget is full.
func (i *Index) BuildContext(ctx context.Context, query string, budget, limit int) (Context, error) {
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	if budget <= 0 {
		budget = 4000
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	candidateLimit := limit * 3
	if candidateLimit > 100 {
		candidateLimit = 100
	}
	result, err := i.Search(ctx, query, candidateLimit)
	if err != nil {
		return Context{}, err
	}
	terms := strings.Fields(strings.ToLower(query))
	tracker := contextBudget{limit: budget}
	selected := make([]string, 0, limit)
	excluded := make([]string, 0, limit)
	selectedSet := map[string]bool{}
	boundedMatches := make([]Match, 0, minInt(limit, len(result.Matches)))
	boundedProvenance := make([]Provenance, 0, cap(boundedMatches))
	for index, match := range result.Matches {
		if err := ctx.Err(); err != nil {
			return Context{}, err
		}
		if len(boundedMatches) >= limit {
			appendUniqueBounded(&excluded, match.File, limit)
			continue
		}
		provenance := Provenance{}
		if index < len(result.Provenance) {
			provenance = result.Provenance[index]
		}
		if !tracker.admit(matchContextCost(match, provenance)) {
			appendUniqueBounded(&excluded, match.File, limit)
			continue
		}
		boundedMatches = append(boundedMatches, match)
		boundedProvenance = append(boundedProvenance, provenance)
		if !selectedSet[match.File] {
			selectedSet[match.File] = true
			selected = append(selected, match.File)
		}
	}
	result.Matches = boundedMatches
	result.Provenance = boundedProvenance

	symbolsByFile := make(map[string][]Symbol)
	for _, symbol := range i.Symbols() {
		symbolsByFile[symbol.File] = append(symbolsByFile[symbol.File], symbol)
	}
	symbols := make([]Symbol, 0)
	for _, file := range selected {
		for _, symbol := range symbolsByFile[file] {
			if !symbolMatchesTerms(symbol, terms) {
				continue
			}
			if tracker.admit(symbolContextCost(symbol)) {
				symbols = append(symbols, symbol)
			}
		}
	}
	for _, file := range i.Files() {
		if !selectedSet[file.Path] {
			appendUniqueBounded(&excluded, file.Path, limit)
		}
	}
	result.Budget, result.Selected, result.Excluded, result.Symbols = budget, selected, excluded, symbols
	if store, err := memory.New(i.root); err == nil {
		if matches, searchErr := store.SearchRanked(query, "", 8); searchErr == nil {
			for _, match := range matches {
				if err := ctx.Err(); err != nil {
					return Context{}, err
				}
				if !tracker.admit(memoryContextCost(match.Entry)) {
					appendUniqueBounded(&result.Excluded, "memory:"+match.Entry.ID, limit)
					continue
				}
				result.Memory = append(result.Memory, match.Entry)
			}
		}
	}
	if store, err := research.New(i.root); err == nil {
		if claims, searchErr := store.Search(query, 8); searchErr == nil {
			for _, claim := range claims {
				if err := ctx.Err(); err != nil {
					return Context{}, err
				}
				if !tracker.admit(claimContextCost(claim)) {
					appendUniqueBounded(&result.Excluded, "claim:"+claim.Claim.ID, limit)
					continue
				}
				result.Claims = append(result.Claims, claim)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Context{}, err
	}
	// Sources are bounded provenance descriptors, not retrieved source content;
	// keep them outside the content budget so callers can always audit where the
	// selected material came from.
	result.Sources = i.sources(ctx, query)
	result.Used = tracker.used
	for idx := range result.Provenance {
		result.Provenance[idx].Reason = "selected by bounded relevance retrieval for query"
	}
	return result, nil
}

type contextBudget struct {
	limit int
	used  int
}

func (b *contextBudget) admit(cost int) bool {
	if cost < 1 {
		cost = 1
	}
	if b.used+cost > b.limit {
		return false
	}
	b.used += cost
	return true
}

// approximateTokens follows the same four-bytes-per-token approximation used
// by prompt assembly. It is tokenizer-independent and deterministic.
func approximateTokens(values ...string) int {
	bytes := 0
	for _, value := range values {
		bytes += len(value)
	}
	if bytes == 0 {
		return 1
	}
	return (bytes + 3) / 4
}

func matchContextCost(match Match, provenance Provenance) int {
	// Hashes, line numbers, and scores are fixed-size integrity metadata; the
	// paths, source text, and selection reason are the variable context payload.
	return 6 + approximateTokens(match.File, match.Text, provenance.File, provenance.Reason)
}

func symbolContextCost(symbol Symbol) int {
	return 3 + approximateTokens(symbol.Name, symbol.Kind, symbol.File)
}

func memoryContextCost(entry memory.Entry) int {
	return 4 + approximateTokens(entry.Category, entry.Scope, entry.Content, strings.Join(entry.Tags, " "), entry.Source)
}

func claimContextCost(result research.SearchResult) int {
	return 4 + approximateTokens(result.Claim.Text, strings.Join(result.Reasons, " "))
}

func symbolMatchesTerms(symbol Symbol, terms []string) bool {
	lower := strings.ToLower(symbol.Name)
	for _, term := range terms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

func appendUniqueBounded(items *[]string, value string, limit int) {
	if value == "" || len(*items) >= limit {
		return
	}
	for _, item := range *items {
		if item == value {
			return
		}
	}
	*items = append(*items, value)
}

func (i *Index) sources(ctx context.Context, query string) []Source {
	sources := []Source{}
	add := func(kind, path, reason string, count int) {
		sources = append(sources, Source{Kind: kind, Path: path, Reason: reason, Count: count})
	}
	if _, err := os.Stat(filepath.Join(i.root, ".rsch", "intelligence", "architecture.json")); err == nil {
		add("architecture", ".rsch/intelligence/architecture.json", "repository architecture graph for impact and structural context", 1)
	}
	specRoot := filepath.Join(i.root, ".rsch", "specs")
	_ = filepath.WalkDir(specRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || len(sources) >= 21 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			rel, _ := filepath.Rel(i.root, path)
			add("specification", filepath.ToSlash(rel), "repository specification relevant to the active context", 1)
		}
		return nil
	})
	if store, err := memory.New(i.root); err == nil {
		if entries, searchErr := store.Search(query, ""); searchErr == nil && len(entries) > 0 {
			add("memory", ".rsch/memory/entries.json", "matching scoped project memory", len(entries))
		}
	}
	if store, err := research.New(i.root); err == nil {
		if claims, searchErr := store.Search(query, 8); searchErr == nil && len(claims) > 0 {
			add("research", ".rsch/research/records.json", "matching claims with source-backed provenance", len(claims))
		}
	}
	if rules, err := steering.Load(i.root); err == nil {
		for _, rule := range rules.Rules {
			rel, relErr := filepath.Rel(i.root, rule.Path)
			path := rule.Path
			if relErr == nil {
				path = filepath.ToSlash(rel)
			}
			add("steering", path, "resolved project steering rule", 1)
		}
	}
	for _, path := range []string{".rsch/skills", ".agents/skills", ".codex/skills"} {
		if info, err := os.Stat(filepath.Join(i.root, path)); err == nil && info.IsDir() {
			add("skills", path, "project or shared skill directory available to the workflow", 1)
		}
	}
	status := "unavailable"
	if result, err := process.RunOutput(ctx, process.Spec{Argv: []string{"git", "-C", i.root, "status", "--short", "--branch"}}); err == nil {
		status = strings.TrimSpace(string(result.Output))
		if status == "" {
			status = "clean"
		}
	}
	add("git", "", "current repository state: "+truncate(status, 240), 1)
	add("tools", "", "RSCH tool boundary available for repository retrieval and verification", 1)
	return sources
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type symbolPattern struct {
	kind    string
	pattern *regexp.Regexp
}

var symbolPatterns = []symbolPattern{
	{kind: "function", pattern: regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?(?:def|function)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)},
	{kind: "function", pattern: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)`)},
	{kind: "type", pattern: regexp.MustCompile(`^\s*(?:export\s+)?(?:pub(?:\([^)]*\))?\s+)?(?:type|class|interface|struct|enum|trait|record)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)},
	{kind: "function", pattern: regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[A-Za-z_$][A-Za-z0-9_$]*)\s*=>`)},
}

func extractSymbols(file string, data []byte) []Symbol {
	if strings.EqualFold(filepath.Ext(file), ".go") {
		if symbols, ok := extractGoSymbols(file, data); ok {
			return symbols
		}
	}
	var out []Symbol
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64*1024), maxIndexedBytes+1)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		for _, pattern := range symbolPatterns {
			match := pattern.pattern.FindStringSubmatch(text)
			if len(match) > 1 {
				out = append(out, Symbol{Name: match[1], Kind: pattern.kind, File: file, Line: line})
				break
			}
		}
	}
	return out
}

func extractGoSymbols(file string, data []byte) ([]Symbol, bool) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, file, data, 0)
	if err != nil {
		return nil, false
	}
	symbols := make([]Symbol, 0)
	add := func(name, kind string, position token.Pos) {
		if name == "" {
			return
		}
		symbols = append(symbols, Symbol{Name: name, Kind: kind, File: file, Line: fileSet.Position(position).Line})
	}
	for _, declaration := range parsed.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			kind := "function"
			if declaration.Recv != nil {
				kind = "method"
			}
			add(declaration.Name.Name, kind, declaration.Name.Pos())
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				switch specification := specification.(type) {
				case *ast.TypeSpec:
					add(specification.Name.Name, "type", specification.Name.Pos())
				case *ast.ValueSpec:
					kind := "variable"
					if declaration.Tok == token.CONST {
						kind = "constant"
					}
					for _, name := range specification.Names {
						add(name.Name, kind, name.Pos())
					}
				}
			}
		}
	}
	return symbols, true
}
func ignored(name string) bool {
	switch name {
	case ".git", ".rsch", ".ssh", ".aws", ".azure", ".gnupg", "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}

type ignoreRule struct {
	base          string
	pattern       string
	negated       bool
	directoryOnly bool
	anchored      bool
}

type ignoreMatcher struct {
	root   string
	loaded map[string]bool
	rules  map[string][]ignoreRule
}

func newIgnoreMatcher(root string) *ignoreMatcher {
	return &ignoreMatcher{root: root, loaded: make(map[string]bool), rules: make(map[string][]ignoreRule)}
}

func (m *ignoreMatcher) ignored(relative string, directory bool) bool {
	relative = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(relative)), "./")
	if relative == "" || relative == "." {
		return false
	}

	ignored := false
	for _, base := range ignoreRuleBases(relative) {
		for _, rule := range m.rulesFor(base) {
			if rule.matches(relative, directory) {
				ignored = !rule.negated
			}
		}
	}
	return ignored
}

func ignoreRuleBases(relative string) []string {
	directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relative)))
	if directory == "." || directory == "" {
		return []string{""}
	}
	parts := strings.Split(directory, "/")
	bases := make([]string, 1, len(parts)+1)
	for index := range parts {
		bases = append(bases, strings.Join(parts[:index+1], "/"))
	}
	return bases
}

func (m *ignoreMatcher) rulesFor(base string) []ignoreRule {
	if m.loaded[base] {
		return m.rules[base]
	}
	m.loaded[base] = true
	ignorePath := filepath.Join(m.root, filepath.FromSlash(base), ".gitignore")
	resolved, withinRoot, err := resolveWithinRoot(m.root, ignorePath)
	if err != nil || !withinRoot {
		return nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}
		rule := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			rule.negated = true
			line = strings.TrimPrefix(line, "!")
		}
		rule.anchored = strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		rule.directoryOnly = strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		rule.pattern = line
		m.rules[base] = append(m.rules[base], rule)
	}
	return m.rules[base]
}

func (r ignoreRule) matches(relative string, directory bool) bool {
	candidate := relative
	if r.base != "" {
		prefix := r.base + "/"
		if !strings.HasPrefix(relative, prefix) {
			return false
		}
		candidate = strings.TrimPrefix(relative, prefix)
	}
	parts := strings.Split(candidate, "/")
	directoryParts := len(parts)
	if !directory {
		directoryParts--
	}

	if !r.anchored && !strings.Contains(r.pattern, "/") {
		limit := len(parts)
		if r.directoryOnly {
			limit = directoryParts
		}
		for _, part := range parts[:limit] {
			if matched, _ := doublestar.PathMatch(r.pattern, part); matched {
				return true
			}
		}
		return false
	}

	if !r.directoryOnly {
		if matched, _ := doublestar.PathMatch(r.pattern, candidate); matched {
			return true
		}
	}
	for count := 1; count <= directoryParts; count++ {
		if matched, _ := doublestar.PathMatch(r.pattern, strings.Join(parts[:count], "/")); matched {
			return true
		}
	}
	return false
}

func resolveWithinRoot(root, candidate string) (string, bool, error) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", false, err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", false, err
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return resolved, false, nil
	}
	return resolved, true, nil
}

// An alias must satisfy the same exclusions as its resolved destination.
// Recheck both paths whenever content is read, since symlinks and ignore
// rules can change after the index is built.
func resolveIndexedPath(root, candidate string, rules *ignoreMatcher) (string, bool, error) {
	resolved, withinRoot, err := resolveWithinRoot(root, candidate)
	if err != nil || !withinRoot {
		return resolved, false, err
	}
	for _, path := range []string{candidate, resolved} {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return "", false, err
		}
		if !filepath.IsLocal(relative) || isSecretPath(relative) || rules.ignored(relative, false) {
			return resolved, false, nil
		}
		for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
			if ignored(part) {
				return resolved, false, nil
			}
		}
	}
	return resolved, true, nil
}

func isSecretPath(relative string) bool {
	name := strings.ToLower(filepath.Base(filepath.FromSlash(relative)))
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return true
	}
	switch name {
	case ".npmrc", ".pypirc", ".netrc", "_netrc", "credentials.json", "service-account.json", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "secrets.json", "secrets.yaml", "secrets.yml", "secrets.toml":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return true
	}
	return false
}

func isIndexable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt", ".c", ".h", ".cpp", ".cc", ".cs", ".rb", ".php", ".swift", ".md", ".json", ".yaml", ".yml", ".toml", ".sql", ".ini", ".cfg", ".xml":
		return true
	}
	return false
}
func language(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "" {
		return ""
	}
	return ext
}
func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return fmt.Sprintf("%s…", value[:max-1])
}
