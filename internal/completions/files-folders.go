package completions

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	"github.com/lithammer/fuzzysearch/fuzzy"
	"github.com/opencode-ai/opencode/internal/fileutil"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

type filesAndFoldersContextGroup struct {
	prefix string
}

func (cg *filesAndFoldersContextGroup) GetId() string {
	return cg.prefix
}

func (cg *filesAndFoldersContextGroup) GetEntry() dialog.CompletionItemI {
	return dialog.NewCompletionItem(dialog.CompletionItem{
		Title: "Files & Folders",
		Value: "files",
	})
}

func processNullTerminatedOutput(outputBytes []byte) []string {
	if len(outputBytes) > 0 && outputBytes[len(outputBytes)-1] == 0 {
		outputBytes = outputBytes[:len(outputBytes)-1]
	}

	if len(outputBytes) == 0 {
		return []string{}
	}

	split := bytes.Split(outputBytes, []byte{0})
	matches := make([]string, 0, len(split))

	for _, p := range split {
		if len(p) == 0 {
			continue
		}

		path := string(p)
		path = filepath.Join(".", path)

		if !fileutil.SkipHidden(path) {
			matches = append(matches, path)
		}
	}

	return matches
}

func (cg *filesAndFoldersContextGroup) getFiles(query string) ([]string, error) {
	rgSpec, _ := fileutil.GetRgSpec("") // No glob pattern for this use case
	fzfSpec, _ := fileutil.GetFzfSpec(query)
	return filesWithCommands(query, rgSpec, fzfSpec)
}

func filesWithCommands(query string, rgSpec, fzfSpec process.Spec) ([]string, error) {
	hasRg := len(rgSpec.Argv) != 0
	hasFzf := len(fzfSpec.Argv) != 0

	var matches []string
	// Case 1: Both rg and fzf available
	if hasRg && hasFzf {
		rgResult, errRg := process.RunOutput(context.Background(), rgSpec)
		if errRg != nil && rgResult.ExitCode != 1 {
			return nil, fmt.Errorf("rg command failed: %w", errRg)
		}
		fzfSpec.Stdin = bytes.NewReader(rgResult.Output)
		fzfResult, errFzf := process.RunOutput(context.Background(), fzfSpec)
		if errFzf != nil {
			if fzfResult.ExitCode == 1 {
				return []string{}, nil // No matches from fzf
			}
			return nil, fmt.Errorf("fzf command failed: %w", errFzf)
		}

		matches = processNullTerminatedOutput(fzfResult.Output)

		// Case 2: Only rg available
	} else if hasRg {
		logging.Debug("Using Ripgrep with fuzzy match fallback for file completions")
		rgResult, err := process.RunOutput(context.Background(), rgSpec)
		if err != nil {
			if rgResult.ExitCode == 1 {
				return []string{}, nil // Ripgrep found no files.
			}
			return nil, fmt.Errorf("rg command failed: %w", err)
		}

		allFiles := processNullTerminatedOutput(rgResult.Output)
		matches = fuzzy.Find(query, allFiles)

		// Case 3: Only fzf available
	} else if hasFzf {
		logging.Debug("Using FZF with doublestar fallback for file completions")
		files, _, err := fileutil.GlobWithDoublestar("**/*", ".", 0)
		if err != nil {
			return nil, fmt.Errorf("failed to list files for fzf: %w", err)
		}

		allFiles := make([]string, 0, len(files))
		for _, file := range files {
			if !fileutil.SkipHidden(file) {
				allFiles = append(allFiles, file)
			}
		}

		var fzfIn bytes.Buffer
		for _, file := range allFiles {
			fzfIn.WriteString(file)
			fzfIn.WriteByte(0)
		}

		fzfSpec.Stdin = &fzfIn
		fzfResult, err := process.RunOutput(context.Background(), fzfSpec)
		if err != nil {
			if fzfResult.ExitCode == 1 {
				return []string{}, nil
			}
			return nil, fmt.Errorf("fzf command failed: %w", err)
		}

		matches = processNullTerminatedOutput(fzfResult.Output)

		// Case 4: Fallback to doublestar with fuzzy match
	} else {
		logging.Debug("Using doublestar with fuzzy match for file completions")
		allFiles, _, err := fileutil.GlobWithDoublestar("**/*", ".", 0)
		if err != nil {
			return nil, fmt.Errorf("failed to glob files: %w", err)
		}

		filteredFiles := make([]string, 0, len(allFiles))
		for _, file := range allFiles {
			if !fileutil.SkipHidden(file) {
				filteredFiles = append(filteredFiles, file)
			}
		}

		matches = fuzzy.Find(query, filteredFiles)
	}

	return matches, nil
}

func (cg *filesAndFoldersContextGroup) GetChildEntries(query string) ([]dialog.CompletionItemI, error) {
	matches, err := cg.getFiles(query)
	if err != nil {
		return nil, err
	}

	items := make([]dialog.CompletionItemI, 0, len(matches))
	for _, file := range matches {
		item := dialog.NewCompletionItem(dialog.CompletionItem{
			Title: file,
			Value: file,
		})
		items = append(items, item)
	}

	return items, nil
}

func NewFileAndFolderContextGroup() dialog.CompletionProvider {
	return &filesAndFoldersContextGroup{
		prefix: "file",
	}
}
