package intelligence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/memory"
)

func TestBuildPersistsAndReusesIndexCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc Main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".rsch", "intelligence", "index.json")); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(root, ".rsch", "intelligence", "index.json")
	cacheInfo, err := os.Stat(cacheFile)
	if err != nil || cacheInfo.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions are not private: %v %v", cacheInfo, err)
	}
	metadata := index.CacheMetadata()
	if metadata.Version != indexCacheVersion || metadata.Scope != indexCacheScope || metadata.Security != indexSecurity || metadata.PolicyHash == "" || metadata.BuiltAt.IsZero() {
		t.Fatalf("incomplete cache metadata: %+v", metadata)
	}
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	var stored cache
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	stored.Metadata.Version = 1
	data, _ = json.Marshal(stored)
	if err := os.WriteFile(cacheFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(second.Files()) != 1 || len(second.Symbols()) != 1 {
		t.Fatalf("cache rebuild lost index: files=%d symbols=%d", len(second.Files()), len(second.Symbols()))
	}
	if second.CacheMetadata().Version != indexCacheVersion {
		t.Fatalf("stale cache metadata was not invalidated: %+v", second.CacheMetadata())
	}
}

func TestBuildChecksWarmCacheBeforeReadingUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\nfunc Main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	index.readFile = func(path string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected content read for unchanged file %s", path)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatalf("warm cache should avoid unchanged file reads: %v", err)
	}
}

func TestBuildRejectsSymlinkEscapesFromWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, []byte("package secret\nconst Exfiltrated = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "escape.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape-directory")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing.go"), filepath.Join(root, "broken.go")); err != nil {
		t.Skipf("broken symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "safe.go"), []byte("package safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	files := index.Files()
	if len(files) != 1 || files[0].Path != "safe.go" {
		t.Fatalf("symlink escape entered repository index: %+v", files)
	}
}

func TestSearchRevalidatesSymlinkBoundaryAfterBuild(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	safeTarget := filepath.Join(root, "safe.go")
	alias := filepath.Join(root, "alias.go")
	if err := os.WriteFile(safeTarget, []byte("package safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(safeTarget, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(outsideFile, []byte("package secret\n// externalneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, alias); err != nil {
		t.Fatal(err)
	}

	result, err := index.Search(context.Background(), "externalneedle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("search followed a post-build symlink escape: %+v", result.Matches)
	}
}

func TestSearchRechecksIgnoreRulesAfterBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sensitive.go"), []byte("package sensitive\n// externalneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("sensitive.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := index.Search(context.Background(), "externalneedle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("search returned a file ignored after the index build: %+v", result.Matches)
	}
}

func TestIndexRejectsAliasesToExcludedTargets(t *testing.T) {
	for _, target := range []string{"credentials.json", ".aws/config.json", "private/data.json"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, target)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, target), []byte(`{"value":"hiddenneedle"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "alias.json")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			index, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := index.Build(context.Background()); err != nil {
				t.Fatal(err)
			}
			if files := index.Files(); len(files) != 0 {
				t.Fatalf("excluded target entered index through alias: %+v", files)
			}
		})
	}
}

func TestSearchRejectsAliasRetargetedToSecretAfterBuild(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"safe.json": "{}", "credentials.json": `{"value":"hiddenneedle"}`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias.json")
	if err := os.Symlink("safe.json", alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("credentials.json", alias); err != nil {
		t.Fatal(err)
	}
	result, err := index.Search(context.Background(), "hiddenneedle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("secret target became searchable through alias: %+v", result.Matches)
	}
}

func TestBuildRespectsLayeredGitIgnoreRules(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		".gitignore":                 "ignored.go\ngenerated-output/\n*.json\n!manifest.json\n",
		"ignored.go":                 "package ignored\n",
		"visible.go":                 "package visible\n",
		"manifest.json":              "{}\n",
		"ignored.json":               "{}\n",
		"generated-output/code.go":   "package generated\n",
		"nested/.gitignore":          "*.go\n!keep.go\n",
		"nested/drop.go":             "package dropped\n",
		"nested/keep.go":             "package kept\n",
		"nested/deeper/also-drop.go": "package dropped\n",
	}
	for name, content := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(index.Files()))
	for _, file := range index.Files() {
		got = append(got, file.Path)
	}
	want := []string{"manifest.json", "nested/keep.go", "visible.go"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("indexed paths = %v, want %v", got, want)
	}
}

func TestBuildAlwaysExcludesSecretFiles(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		".env":                "API_TOKEN=externalneedle\n",
		".env.production":     "API_TOKEN=externalneedle\n",
		"credentials.json":    `{"token":"externalneedle"}`,
		"config/private.pem":  "externalneedle\n",
		"config/secrets.yaml": "token: externalneedle\n",
		"main.go":             "package main\n",
	}
	for name, content := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	files := index.Files()
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("secret-bearing files entered repository index: %+v", files)
	}
	result, err := index.Search(context.Background(), "externalneedle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("secret-bearing content was searchable: %+v", result.Matches)
	}
}

func TestBuildContextIncludesLayeredProvenanceWithinBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc HandleOAuth() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, ".rsch", "intelligence"), filepath.Join(root, ".rsch", "specs", "oauth"), filepath.Join(root, ".rsch", "steering")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "intelligence", "architecture.json"), []byte(`{"nodes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "specs", "oauth", "requirements.md"), []byte("OAuth requirements"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".rsch", "steering", "repository.md"), []byte("verify changes"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := memory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(memory.Entry{Content: "OAuth must be verified", Tags: []string{"OAuth"}}); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := index.BuildContext(context.Background(), "OAuth", 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Budget != 100 || len(result.Sources) < 4 {
		t.Fatalf("missing layered provenance: %+v", result)
	}
	if len(result.Memory) != 1 || !strings.Contains(result.Memory[0].Content, "OAuth") {
		t.Fatalf("memory content was not included in bounded context: %+v", result.Memory)
	}
	if result.Used > result.Budget {
		t.Fatalf("context exceeded budget: used=%d budget=%d", result.Used, result.Budget)
	}
}

func TestIndexBuildsSearchableProvenance(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\nfunc HandleOAuth() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := index.Search(context.Background(), "OAuth", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || len(result.Provenance) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Matches[0].Score < 1 || !strings.Contains(result.Provenance[0].Reason, "relevance-ranked") {
		t.Fatalf("search relevance was not recorded: %+v", result)
	}
}

func TestSearchKeepsOnlyTopRankedMatchesWithPairedProvenance(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"a.go": "package sample\n// needle\n",
		"b.go": "package sample\n// needle needle needle needle needle needle\n",
		"c.go": "package sample\n// needle needle\n",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}

	result, err := index.Search(context.Background(), "needle", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || len(result.Provenance) != 2 {
		t.Fatalf("top-K result sizes = matches:%d provenance:%d, want 2/2", len(result.Matches), len(result.Provenance))
	}
	if result.Matches[0].File != "b.go" || result.Matches[1].File != "c.go" {
		t.Fatalf("top-K ranking = %+v, want b.go then c.go", result.Matches)
	}
	files := map[string]File{}
	for _, file := range index.Files() {
		files[file.Path] = file
	}
	for idx, match := range result.Matches {
		provenance := result.Provenance[idx]
		if provenance.File != match.File || provenance.Line != match.Line || provenance.Hash != files[match.File].Hash {
			t.Fatalf("provenance[%d] = %+v does not describe match %+v", idx, provenance, match)
		}
	}
}

func TestBuildContextBudgetConstrainsReturnedMatches(t *testing.T) {
	root := t.TempDir()
	content := "package sample\n// needle one\n// needle two\n// needle three\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	candidates, err := index.Search(context.Background(), "needle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.Matches) < 2 {
		t.Fatalf("need multiple search candidates, got %+v", candidates.Matches)
	}
	budget := matchContextCost(candidates.Matches[0], candidates.Provenance[0])

	result, err := index.BuildContext(context.Background(), "needle", budget, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Used > result.Budget {
		t.Fatalf("context used %d tokens with budget %d", result.Used, result.Budget)
	}
	if len(result.Matches) != 1 || len(result.Provenance) != 1 {
		t.Fatalf("budget admitted matches=%d provenance=%d, want exactly one paired result", len(result.Matches), len(result.Provenance))
	}
	if result.Selected[0] != result.Matches[0].File {
		t.Fatalf("selected files %+v do not describe returned match %+v", result.Selected, result.Matches[0])
	}
}

func TestSearchHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package sample\n// needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := index.Search(ctx, "needle", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search error = %v, want context.Canceled", err)
	}
}

func TestGoSymbolExtractionUsesSyntaxTree(t *testing.T) {
	root := t.TempDir()
	content := `package sample

const DefaultLimit = 10
var Enabled = true
type Service struct{}
func NewService() *Service { return &Service{} }
func (Service) Run() {}
`
	if err := os.WriteFile(filepath.Join(root, "service.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DefaultLimit": "constant", "Enabled": "variable", "Service": "type", "NewService": "function", "Run": "method"}
	for _, symbol := range index.Symbols() {
		if kind, ok := want[symbol.Name]; ok {
			if symbol.Kind != kind {
				t.Fatalf("symbol %s kind = %q, want %q", symbol.Name, symbol.Kind, kind)
			}
			delete(want, symbol.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing parsed Go symbols: %+v", want)
	}
}

func BenchmarkSearchTopK(b *testing.B) {
	root := b.TempDir()
	for file := 0; file < 100; file++ {
		var content strings.Builder
		content.WriteString("package sample\n")
		for line := 0; line < 100; line++ {
			fmt.Fprintf(&content, "// repository needle %d %d\n", file, line)
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file_%03d.go", file)), []byte(content.String()), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	index, err := New(root)
	if err != nil {
		b.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := index.Search(context.Background(), "repository needle", 20); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildWarmCache(b *testing.B) {
	root := b.TempDir()
	for file := 0; file < 100; file++ {
		content := fmt.Sprintf("package sample\nfunc Function%d() {}\n", file)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file_%03d.go", file)), []byte(content), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	index, err := New(root)
	if err != nil {
		b.Fatal(err)
	}
	if err := index.Build(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if err := index.Build(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
