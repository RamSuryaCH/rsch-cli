package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetContextFromPaths(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := config.Load(tmpDir, false)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	cfg := config.Get()
	cfg.WorkingDir = tmpDir
	cfg.ContextPaths = []string{
		"file.txt",
		"directory/",
	}
	testFiles := []string{
		"file.txt",
		"directory/file_a.txt",
		"directory/file_b.txt",
		"directory/file_c.txt",
	}

	createTestFiles(t, tmpDir, testFiles)
	canonicalTmpDir, err := filepath.EvalSymlinks(tmpDir)
	require.NoError(t, err)

	context := getContextFromPaths()
	expectedContext := fmt.Sprintf("# From:%s/file.txt\nfile.txt: test content\n# From:%s/directory/file_a.txt\ndirectory/file_a.txt: test content\n# From:%s/directory/file_b.txt\ndirectory/file_b.txt: test content\n# From:%s/directory/file_c.txt\ndirectory/file_c.txt: test content", canonicalTmpDir, canonicalTmpDir, canonicalTmpDir, canonicalTmpDir)
	assert.Equal(t, expectedContext, context)
}

func TestProcessContextPathsPreservesConfiguredAndHierarchicalPrecedence(t *testing.T) {
	repository := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repository, ".git"), 0755))
	target := filepath.Join(repository, "services", "api")
	require.NoError(t, os.MkdirAll(target, 0755))

	rootAgents := filepath.Join(repository, agentsFileName)
	serviceAgents := filepath.Join(repository, "services", agentsFileName)
	targetAgents := filepath.Join(target, agentsFileName)
	require.NoError(t, os.WriteFile(rootAgents, []byte("root instructions"), 0644))
	require.NoError(t, os.WriteFile(serviceAgents, []byte("service instructions"), 0644))
	require.NoError(t, os.WriteFile(targetAgents, []byte("target instructions"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(target, "z.txt"), []byte("configured z"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(target, "a.txt"), []byte("configured a"), 0644))

	context := processContextPaths(target, []string{"z.txt", "a.txt"})
	rootIndex := strings.Index(context, "root instructions")
	serviceIndex := strings.Index(context, "service instructions")
	targetIndex := strings.Index(context, "target instructions")
	zIndex := strings.Index(context, "configured z")
	aIndex := strings.Index(context, "configured a")
	require.NotEqual(t, -1, rootIndex)
	require.NotEqual(t, -1, serviceIndex)
	require.NotEqual(t, -1, targetIndex)
	require.NotEqual(t, -1, zIndex)
	require.NotEqual(t, -1, aIndex)
	assert.Less(t, rootIndex, serviceIndex)
	assert.Less(t, serviceIndex, targetIndex)
	assert.Less(t, targetIndex, zIndex)
	assert.Less(t, zIndex, aIndex)
}

func TestHierarchicalInstructionsPreferOverrideInSameDirectory(t *testing.T) {
	repository := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repository, ".git"), 0755))
	target := filepath.Join(repository, "nested")
	require.NoError(t, os.MkdirAll(target, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repository, agentsFileName), []byte("shadowed root"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repository, agentsOverrideFileName), []byte("root override"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(target, agentsFileName), []byte("nested instructions"), 0644))

	context := processContextPaths(target, nil)
	assert.NotContains(t, context, "shadowed root")
	rootIndex := strings.Index(context, "root override")
	nestedIndex := strings.Index(context, "nested instructions")
	require.NotEqual(t, -1, rootIndex)
	require.NotEqual(t, -1, nestedIndex)
	assert.Less(t, rootIndex, nestedIndex)

	discovered, err := DiscoverInstructionFiles(target)
	require.NoError(t, err)
	canonicalOverride, err := filepath.EvalSymlinks(filepath.Join(repository, agentsOverrideFileName))
	require.NoError(t, err)
	canonicalNested, err := filepath.EvalSymlinks(filepath.Join(target, agentsFileName))
	require.NoError(t, err)
	assert.Equal(t, []string{canonicalOverride, canonicalNested}, discovered)
}

func TestConfiguredAgentsPathCannotBypassOverride(t *testing.T) {
	repository := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repository, ".git"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repository, agentsFileName), []byte("shadowed root"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repository, agentsOverrideFileName), []byte("root override"), 0644))

	context := processProjectContext(repository, []string{agentsFileName})
	assert.Contains(t, context.Instructions, "root override")
	assert.NotContains(t, context.Instructions, "shadowed root")
	assert.NotContains(t, context.Data, "shadowed root")
}

func TestDirectoryContextDoesNotPromoteNestedInstructionBasenames(t *testing.T) {
	workspace := t.TempDir()
	vendor := filepath.Join(workspace, "vendor", "package")
	require.NoError(t, os.MkdirAll(vendor, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(vendor, "CLAUDE.md"), []byte("nested claude data"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(vendor, agentsFileName), []byte("out-of-scope agents"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(vendor, "notes.txt"), []byte("ordinary data"), 0644))

	context := processProjectContext(workspace, []string{"vendor/"})
	assert.Empty(t, context.Instructions)
	assert.Contains(t, context.Data, "nested claude data")
	assert.Contains(t, context.Data, "ordinary data")
	assert.NotContains(t, context.Data, "out-of-scope agents")
}

func TestDiscoverInstructionFilesRejectsInvalidWorkingDirectory(t *testing.T) {
	_, err := DiscoverInstructionFiles(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
}

func TestHierarchicalInstructionsRejectSymlinkEscape(t *testing.T) {
	repository := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repository, ".git"), 0755))
	target := filepath.Join(repository, "nested")
	require.NoError(t, os.MkdirAll(target, 0755))
	outside := filepath.Join(t.TempDir(), agentsFileName)
	require.NoError(t, os.WriteFile(outside, []byte("outside instructions"), 0644))
	if err := os.Symlink(outside, filepath.Join(target, agentsFileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	context := processContextPaths(target, nil)
	assert.NotContains(t, context, "outside instructions")
}

func TestHierarchicalInstructionsRefreshAndRemainBounded(t *testing.T) {
	repository := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repository, ".git"), 0755))
	agentsPath := filepath.Join(repository, agentsFileName)
	require.NoError(t, os.WriteFile(agentsPath, []byte("first instructions"), 0644))
	assert.Contains(t, processContextPaths(repository, nil), "first instructions")

	require.NoError(t, os.WriteFile(agentsPath, []byte("second instructions"), 0644))
	refreshed := processContextPaths(repository, nil)
	assert.NotContains(t, refreshed, "first instructions")
	assert.Contains(t, refreshed, "second instructions")

	require.NoError(t, os.WriteFile(agentsPath, []byte(strings.Repeat("界", maxContextBytes)), 0644))
	bounded := processContextPaths(repository, nil)
	canonicalAgentsPath, err := filepath.EvalSymlinks(agentsPath)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(bounded), contextLimitBytes())
	assert.Contains(t, bounded, "# From:"+canonicalAgentsPath)
	assert.Contains(t, bounded, contextTruncated)
	assert.True(t, utf8.ValidString(bounded))
}

func TestLargeRootInstructionsDoNotCrowdOutTargetInstructions(t *testing.T) {
	repository := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repository, ".git"), 0755))
	target := filepath.Join(repository, "nested")
	require.NoError(t, os.MkdirAll(target, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repository, agentsFileName), []byte(strings.Repeat("root ", maxContextBytes)), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(target, agentsFileName), []byte("target survives"), 0644))

	context := processContextPaths(target, nil)
	assert.LessOrEqual(t, len(context), contextLimitBytes())
	assert.Contains(t, context, contextTruncated)
	assert.Contains(t, context, "target survives")
	assert.Less(t, strings.Index(context, "root "), strings.Index(context, "target survives"))
}

func TestProcessContextPathsIsStableUnderRepeatedConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	createTestFiles(t, tmpDir, []string{
		"z.txt",
		"a.txt",
		"directory/file_b.txt",
		"directory/file_a.txt",
	})

	paths := []string{"z.txt", "directory/", "a.txt", "directory/", "z.txt"}
	want := processContextPaths(tmpDir, paths)
	for i := 0; i < 100; i++ {
		assert.Equal(t, want, processContextPaths(tmpDir, paths))
	}

	const callers = 32
	results := make(chan string, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- processContextPaths(tmpDir, paths)
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		assert.Equal(t, want, got)
	}
}

func TestContextDeduplicationPreservesCaseDistinctFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths are case-insensitive")
	}
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "A.txt"), []byte("upper-case path"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("lower-case path"), 0644))
	upperInfo, err := os.Stat(filepath.Join(workspace, "A.txt"))
	require.NoError(t, err)
	lowerInfo, err := os.Stat(filepath.Join(workspace, "a.txt"))
	require.NoError(t, err)
	if os.SameFile(upperInfo, lowerInfo) {
		t.Skip("filesystem is case-insensitive")
	}

	context := processContextPaths(workspace, []string{"A.txt", "a.txt"})
	assert.Contains(t, context, "upper-case path")
	assert.Contains(t, context, "lower-case path")
}

func TestProcessContextPathsRejectsEscapesAndSymlinkFiles(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("outside content"), 0644))
	insideFile := filepath.Join(workspace, "inside.txt")
	require.NoError(t, os.WriteFile(insideFile, []byte("inside content"), 0644))

	paths := []string{insideFile, outsideFile, filepath.Join(workspace, "..", filepath.Base(outside), "outside.txt")}
	context := processContextPaths(workspace, paths)
	assert.Contains(t, context, "inside content")
	assert.NotContains(t, context, "outside content")

	link := filepath.Join(workspace, "outside-link.txt")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	context = processContextPaths(workspace, []string{link})
	assert.Empty(t, context)
}

func TestProcessContextPathsTruncatesAtSafeBoundaries(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "large.txt")
	content := strings.Repeat("界", maxContextBytes)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	context := processContextPaths(workspace, []string{"large.txt"})
	assert.LessOrEqual(t, len(context), contextLimitBytes())
	assert.LessOrEqual(t, (len(context)+3)/4, maxContextTokens)
	assert.Contains(t, context, contextTruncated)
	assert.True(t, utf8.ValidString(context))
}

func TestGetContextFromPathsDoesNotUseStaleGlobalCache(t *testing.T) {
	workspace := t.TempDir()
	first := filepath.Join(workspace, "first.txt")
	second := filepath.Join(workspace, "second.txt")
	require.NoError(t, os.WriteFile(first, []byte("first content"), 0644))
	require.NoError(t, os.WriteFile(second, []byte("second content"), 0644))

	cfg := config.Get()
	if cfg == nil {
		var err error
		cfg, err = config.Load(workspace, false)
		require.NoError(t, err)
	}
	cfg.WorkingDir = workspace
	cfg.ContextPaths = []string{"first.txt"}
	assert.Contains(t, getContextFromPaths(), "first content")

	cfg.ContextPaths = []string{"second.txt"}
	context := getContextFromPaths()
	assert.NotContains(t, context, "first content")
	assert.Contains(t, context, "second content")
}

func createTestFiles(t *testing.T, tmpDir string, testFiles []string) {
	t.Helper()
	for _, path := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		if path[len(path)-1] == '/' {
			err := os.MkdirAll(fullPath, 0755)
			require.NoError(t, err)
		} else {
			dir := filepath.Dir(fullPath)
			err := os.MkdirAll(dir, 0755)
			require.NoError(t, err)
			err = os.WriteFile(fullPath, []byte(path+": test content"), 0644)
			require.NoError(t, err)
		}
	}
}
