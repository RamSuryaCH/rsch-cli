package dialog

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestHelpViewIsIdempotentAndBounded(t *testing.T) {
	help := NewHelpCmp().(*helpCmp)
	help.SetBindings([]key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	})
	help.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	first := help.View()
	second := help.View()
	if first != second {
		t.Fatal("Help.View mutated component geometry between frames")
	}
	if got := lipgloss.Width(first); got > 80 {
		t.Fatalf("help width = %d, want <= 80", got)
	}
}
