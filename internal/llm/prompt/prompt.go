package prompt

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/security"
)

const (
	// Context is prompt input, so keep it bounded independently of the model's
	// configured output budget. The token limit is a deliberately conservative
	// byte-based estimate (roughly four bytes per token).
	maxContextBytes  = 64 * 1024
	maxContextTokens = 16 * 1024
	contextTruncated = "\n[context truncated]\n"
)

func GetAgentPrompt(agentName config.AgentName, provider models.ModelProvider) string {
	basePrompt := ""
	switch agentName {
	case config.AgentCoder:
		basePrompt = CoderPrompt(provider)
	case config.AgentTitle:
		basePrompt = TitlePrompt(provider)
	case config.AgentTask:
		basePrompt = TaskPrompt(provider)
	case config.AgentSummarizer:
		basePrompt = SummarizerPrompt(provider)
	default:
		basePrompt = "You are a helpful assistant"
	}

	if agentName == config.AgentCoder || agentName == config.AgentTask {
		content := getProjectContext()
		logging.Debug("Project context loaded", "instruction_bytes", len(content.Instructions), "data_bytes", len(content.Data))
		blocks := wrapProjectContentBlocks(content, config.WorkspaceTrusted())
		if len(blocks) > 0 {
			return fmt.Sprintf("%s\n\n%s", basePrompt, strings.Join(blocks, "\n\n"))
		}
	}
	return basePrompt
}

func getContextFromPaths() string {
	content := getProjectContext()
	return joinProjectContext(content)
}

type projectContextContent struct {
	Instructions string
	Data         string
}

func getProjectContext() projectContextContent {
	cfg := config.Get()
	if cfg == nil {
		return projectContextContent{}
	}

	return processProjectContext(cfg.WorkingDir, cfg.ContextPaths)
}

func processContextPaths(workDir string, paths []string) string {
	return joinProjectContext(processProjectContext(workDir, paths))
}

func joinProjectContext(content projectContextContent) string {
	parts := make([]string, 0, 2)
	if content.Instructions != "" {
		parts = append(parts, content.Instructions)
	}
	if content.Data != "" {
		parts = append(parts, content.Data)
	}
	return strings.Join(parts, "\n")
}

func processProjectContext(workDir string, paths []string) projectContextContent {
	// Repository instructions are deliberately admitted first so a large
	// optional context file cannot crowd them out of the bounded prompt. Their
	// root-to-working-directory order is preserved by the collector.
	instructionFiles := dedupeContextFiles(collectHierarchicalInstructionFiles(workDir))
	configuredFiles := collectContextFiles(workDir, paths)
	explicitInstructionFiles := configuredInstructionFileSet(workDir, paths)
	dataFiles := make([]string, 0, len(configuredFiles))
	for _, file := range configuredFiles {
		// AGENTS files are governed exclusively by hierarchical discovery. This
		// keeps a configured path (including a recursively scanned directory) from
		// reintroducing a shadowed or out-of-scope AGENTS.md.
		if isAgentsInstructionFile(file) {
			continue
		}
		_, explicitlyConfigured := explicitInstructionFiles[contextPathKey(file)]
		if explicitlyConfigured || isCursorRuleFile(workDir, file) {
			instructionFiles = mergeContextFiles(instructionFiles, []string{file})
			continue
		}
		dataFiles = append(dataFiles, file)
	}
	var builder strings.Builder
	wroteFile := false
	contextLimit := contextLimitBytes()

	for index, filePath := range instructionFiles {
		remaining := contextLimit - builder.Len()
		// Share the remaining budget across all remaining instruction files. This
		// prevents a large root file from hiding a more specific file closer to
		// the working directory.
		allowance := remaining / (len(instructionFiles) - index)
		_, ok, _ := appendContextFile(&builder, filePath, allowance, wroteFile)
		if !ok {
			continue
		}
		wroteFile = true
	}
	instructionEnd := builder.Len()

	for _, filePath := range dataFiles {
		remaining := contextLimit - builder.Len()
		truncated, ok, insufficientSpace := appendContextFile(&builder, filePath, remaining, wroteFile)
		if !ok {
			if insufficientSpace && remaining > 0 {
				combined := appendContextTruncation(builder.String())
				return splitProjectContext(combined, instructionEnd)
			}
			continue
		}
		wroteFile = true
		if truncated {
			break
		}
	}
	return splitProjectContext(builder.String(), instructionEnd)
}

func configuredInstructionFileSet(workDir string, paths []string) map[string]struct{} {
	files := make(map[string]struct{})
	for _, configuredPath := range paths {
		if configuredPath == "" || strings.HasSuffix(configuredPath, "/") || strings.HasSuffix(configuredPath, string(filepath.Separator)) {
			continue
		}
		resolvedPath, err := security.ResolveWorkspacePath(workDir, configuredPath)
		if err != nil || !isConfiguredInstructionFile(workDir, resolvedPath) {
			continue
		}
		files[contextPathKey(resolvedPath)] = struct{}{}
	}
	return files
}

func isCursorRuleFile(workDir, file string) bool {
	root, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, file)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	relative = strings.ToLower(filepath.ToSlash(relative))
	return strings.HasPrefix(relative, ".cursor/rules/") &&
		(strings.HasSuffix(relative, ".md") || strings.HasSuffix(relative, ".mdc"))
}

func splitProjectContext(combined string, instructionEnd int) projectContextContent {
	if instructionEnd > len(combined) {
		instructionEnd = len(combined)
	}
	return projectContextContent{
		Instructions: combined[:instructionEnd],
		Data:         strings.TrimPrefix(combined[instructionEnd:], "\n"),
	}
}

func appendContextFile(builder *strings.Builder, filePath string, maxBytes int, prependNewline bool) (bool, bool, bool) {
	headerPrefix := ""
	if prependNewline {
		headerPrefix = "\n"
	}
	header := headerPrefix + "# From:" + filePath + "\n"
	contentLimit := maxBytes - len(header)
	if contentLimit <= 0 {
		return false, false, true
	}

	content, truncated, ok := readContextFile(filePath, contentLimit)
	if !ok {
		return false, false, false
	}
	if truncated {
		if contentLimit < len(contextTruncated) {
			return false, false, true
		}
		content = truncateUTF8(content, contentLimit-len(contextTruncated)) + contextTruncated
	}
	builder.WriteString(header)
	builder.WriteString(content)
	return truncated, true, false
}

func contextLimitBytes() int {
	limit := maxContextBytes
	if tokenLimit := maxContextTokens * 4; tokenLimit < limit {
		limit = tokenLimit
	}
	return limit
}

func collectContextFiles(workDir string, paths []string) []string {
	files := make([]string, 0, len(paths))
	for _, configuredPath := range paths {
		if configuredPath == "" {
			continue
		}

		resolvedPath, err := security.ResolveWorkspacePath(workDir, configuredPath)
		if err != nil {
			continue
		}

		if strings.HasSuffix(configuredPath, "/") || strings.HasSuffix(configuredPath, string(filepath.Separator)) {
			err = filepath.WalkDir(resolvedPath, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}

				// WalkDir does not follow directory symlinks, but it does return
				// symlinked files. Resolve every file before reading it so those
				// files cannot escape the workspace.
				canonicalPath, resolveErr := security.ResolveWorkspacePath(workDir, path)
				if resolveErr == nil {
					files = append(files, canonicalPath)
				}
				return nil
			})
			if err != nil {
				continue
			}
			continue
		}

		files = append(files, resolvedPath)
	}

	// filepath.WalkDir is deterministic within a directory. Keep the caller's
	// path order intact rather than globally sorting and destroying precedence.
	return dedupeContextFiles(files)
}

func readContextFile(filePath string, maxBytes int) (string, bool, bool) {
	// Context must come from ordinary files. Opening a named pipe can block
	// before the size-limited reader is even constructed.
	info, err := os.Stat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		return "", false, false
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", false, false
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false, false
	}

	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", false, false
	}
	if len(data) <= maxBytes {
		return string(data), false, true
	}
	return truncateUTF8(string(data), maxBytes), true, true
}

func appendContextTruncation(value string) string {
	available := contextLimitBytes() - len(contextTruncated)
	return truncateUTF8(value, available) + contextTruncated
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}

	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func processFile(filePath string) string {
	header := "# From:" + filePath + "\n"
	content, truncated, ok := readContextFile(filePath, contextLimitBytes()-len(header))
	if !ok {
		return ""
	}
	result := header + content
	if truncated {
		return appendContextTruncation(result)
	}
	return result
}
