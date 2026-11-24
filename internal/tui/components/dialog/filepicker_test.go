package dialog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/config"
)

func emptyFilepickerForTest(path string) *filepickerCmp {
	cwd := textinput.New()
	cwd.SetValue(path)
	return &filepickerCmp{
		cwdDetails:  &DirNode{directory: path},
		cursorChain: make(stack, 0),
		viewport:    viewport.New(0, 0),
		cwd:         cwd,
	}
}

func TestFilepickerEmptyDirectoryDoesNotIndexSelection(t *testing.T) {
	for _, keyMsg := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
	} {
		picker := emptyFilepickerForTest(t.TempDir())
		if _, _ = picker.Update(keyMsg); picker.hasSelection() {
			t.Fatalf("empty picker unexpectedly has a selection after %q", keyMsg.String())
		}
	}
}

func TestFilepickerFitsCommonTerminalSizes(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		{Width: 40, Height: 10},
		{Width: 80, Height: 24},
		{Width: 120, Height: 30},
	} {
		picker := emptyFilepickerForTest(t.TempDir())
		updated, _ := picker.Update(size)
		view := updated.(*filepickerCmp).View()
		if got := lipgloss.Width(view); got > size.Width {
			t.Errorf("view width at %dx%d = %d", size.Width, size.Height, got)
		}
		if got := lipgloss.Height(view); got > size.Height {
			t.Errorf("view height at %dx%d = %d", size.Width, size.Height, got)
		}
	}
}

func TestFilepickerResizePreservesValidSelection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(dir+"/a", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir+"/b", 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	picker := emptyFilepickerForTest(dir)
	picker.dirs = entries
	picker.cursor = 1

	updated, _ := picker.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := updated.(*filepickerCmp).cursor; got != 1 {
		t.Fatalf("resize moved cursor to %d, want 1", got)
	}
}

func TestFilepickerAdvertisesFunctionKeyWithoutStealingWordForward(t *testing.T) {
	keys := filePickerKeyMap.OpenFilePicker.Keys()
	if len(keys) != 1 || keys[0] != "f5" {
		t.Fatalf("file picker open keys = %v, want [f5]", keys)
	}
	for _, shortcut := range keys {
		if shortcut == "alt+f" {
			t.Fatal("file picker still steals the standard word-forward chord")
		}
	}
}

func TestFilepickerPreviewCommandMutatesOnlyThroughUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.png")
	if err := os.WriteFile(path, []byte("not decoded by the test loader"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	picker := emptyFilepickerForTest(dir)
	picker.dirs = entries
	picker.viewport.Width = 24
	picker.viewport.Height = 4
	picker.previewLoader = func(width int, selectedPath string) (string, error) {
		if width != 20 {
			t.Fatalf("preview width = %d, want 20", width)
		}
		if selectedPath != path {
			t.Fatalf("preview path = %q, want %q", selectedPath, path)
		}
		return "rendered preview", nil
	}

	cmd := picker.loadCurrentPreview()
	if cmd == nil {
		t.Fatal("supported image selection did not schedule a preview command")
	}
	if got := ansi.Strip(picker.viewport.View()); !strings.Contains(got, "Loading preview") {
		t.Fatalf("preview did not enter loading state: %q", got)
	}

	result := cmd()
	if got := ansi.Strip(picker.viewport.View()); strings.Contains(got, "rendered preview") {
		t.Fatalf("preview command mutated model state directly: %q", got)
	}

	updated, followUp := picker.Update(result)
	if followUp != nil {
		t.Fatal("applying a preview result unexpectedly scheduled another command")
	}
	if got := ansi.Strip(updated.(*filepickerCmp).viewport.View()); !strings.Contains(got, "rendered preview") {
		t.Fatalf("current preview result was not applied through Update: %q", got)
	}
}

func TestFilepickerPreviewIgnoresStaleGenerationAndSelection(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.png", "b.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	picker := emptyFilepickerForTest(dir)
	picker.dirs = entries
	picker.viewport.Width = 24
	picker.viewport.Height = 4
	picker.previewLoader = func(_ int, selectedPath string) (string, error) {
		return "preview:" + filepath.Base(selectedPath), nil
	}

	picker.cursor = 0
	firstCmd := picker.loadCurrentPreview()
	picker.cursor = 1
	secondCmd := picker.loadCurrentPreview()
	firstResult := firstCmd().(filePreviewResultMsg)
	secondResult := secondCmd().(filePreviewResultMsg)

	picker.Update(firstResult)
	if got := ansi.Strip(picker.viewport.View()); strings.Contains(got, "preview:a.png") {
		t.Fatalf("stale preview generation replaced current loading state: %q", got)
	}

	wrongPath := secondResult
	wrongPath.path = firstResult.path
	picker.Update(wrongPath)
	if got := ansi.Strip(picker.viewport.View()); strings.Contains(got, "preview:b.png") {
		t.Fatalf("mismatched preview path was applied: %q", got)
	}

	// Even a correctly tagged message must be ignored if the selection moved
	// without scheduling another request.
	picker.cursor = 0
	picker.Update(secondResult)
	if got := ansi.Strip(picker.viewport.View()); strings.Contains(got, "preview:b.png") {
		t.Fatalf("preview for a non-current selection was applied: %q", got)
	}

	picker.cursor = 1
	picker.Update(secondResult)
	if got := ansi.Strip(picker.viewport.View()); !strings.Contains(got, "preview:b.png") {
		t.Fatalf("current preview result was not applied: %q", got)
	}
}

func TestFilepickerViewSanitizesPathsAndFilenames(t *testing.T) {
	dir := t.TempDir()
	hostileName := "safe\x1b]0;spoofed\a\x1b[2J\u202eevil\timage.png"
	if err := os.WriteFile(filepath.Join(dir, hostileName), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	picker := emptyFilepickerForTest(dir)
	picker.width = 80
	picker.height = 24
	picker.dirs = entries
	picker.cwd.SetValue(dir + "/\x1b]52;c;c2VjcmV0\a\u2066")
	view := picker.View()

	assertNoInjectedTerminalControls(t, view)
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "safeevil image.png") {
		t.Fatalf("sanitized filename is not visible: %q", plain)
	}
}

func TestFilepickerDefaultsToConfiguredWorkspace(t *testing.T) {
	workspace := config.WorkingDirectory()
	if !isExistingDirectory(workspace) {
		t.Skip("configured workspace is not an existing directory")
	}

	picker := NewFilepickerCmp(nil).(*filepickerCmp)
	if got := filepath.Clean(picker.cwdDetails.directory); got != filepath.Clean(workspace) {
		t.Fatalf("filepicker start directory = %q, want workspace %q", got, workspace)
	}
}

func assertNoInjectedTerminalControls(t *testing.T, value string) {
	t.Helper()
	for _, unsafe := range []string{"\x1b]", "\a", "\x1b[2J", "\r", "\b", "\t", "\u202e", "\u2066"} {
		if strings.Contains(value, unsafe) {
			t.Fatalf("rendered value contains unsafe sequence %q: %q", unsafe, value)
		}
	}
}
