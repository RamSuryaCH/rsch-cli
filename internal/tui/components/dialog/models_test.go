package dialog

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

func TestModelDialogSelectionRemainsVisibleWithoutColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("RSCH_ASCII", "")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")

	originalProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(originalProfile) })

	dialog := &modelDialogCmp{
		provider:    models.ModelProvider("example"),
		selectedIdx: 1,
		models: []models.Model{
			{Name: "First model"},
			{Name: "Selected model"},
		},
	}

	view := dialog.View()
	if !strings.Contains(view, styles.SelectionLabel("Selected model", true)) {
		t.Fatalf("selected model has no non-color marker: %q", view)
	}
	if !strings.Contains(view, styles.SelectionLabel("First model", false)) {
		t.Fatalf("unselected model alignment changed: %q", view)
	}
}

func TestModelDialogEmptyStateDoesNotSelectOrPanic(t *testing.T) {
	dialog := &modelDialogCmp{}

	dialog.moveSelectionUp()
	dialog.moveSelectionDown()
	dialog.switchProvider(1)
	if dialog.selectedIdx != 0 || dialog.scrollOffset != 0 || dialog.hScrollOffset != 0 {
		t.Fatalf("empty dialog indexes changed: selected=%d scroll=%d provider=%d", dialog.selectedIdx, dialog.scrollOffset, dialog.hScrollOffset)
	}

	updated, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated != dialog {
		t.Fatal("empty dialog update returned a different model")
	}
	if cmd != nil {
		t.Fatal("empty dialog should not emit a model selection command")
	}
	if view := dialog.View(); !strings.Contains(view, "No enabled model providers") {
		t.Fatalf("empty dialog did not explain its state: %q", view)
	}
}

func TestModelHelpersHandleMissingConfiguration(t *testing.T) {
	if got := GetSelectedModel(nil); got != (models.Model{}) {
		t.Fatalf("GetSelectedModel(nil) = %#v, want zero model", got)
	}
	if got := getEnabledProviders(nil); got != nil {
		t.Fatalf("getEnabledProviders(nil) = %#v, want nil", got)
	}
}
