package page

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/opencode-ai/opencode/internal/tui/layout"
)

type recordingLogPane struct {
	focused   bool
	keyEvents int
}

func (p *recordingLogPane) Init() tea.Cmd { return nil }
func (p *recordingLogPane) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		p.keyEvents++
	}
	return p, nil
}
func (p *recordingLogPane) View() string               { return "" }
func (p *recordingLogPane) SetSize(int, int) tea.Cmd   { return nil }
func (p *recordingLogPane) GetSize() (int, int)        { return 0, 0 }
func (p *recordingLogPane) BindingKeys() []key.Binding { return nil }
func (p *recordingLogPane) SetFocused(focused bool)    { p.focused = focused }
func (p *recordingLogPane) IsFocused() bool            { return p.focused }

func TestLogsPageSwitchesAndExclusivelyRoutesPaneFocus(t *testing.T) {
	table := &recordingLogPane{}
	details := &recordingLogPane{}
	page := &logsPage{
		table:           layout.NewContainer(table),
		details:         layout.NewContainer(details),
		tableComponent:  table,
		detailComponent: details,
		focusedPane:     logsTablePane,
	}
	page.syncFocus()

	if !table.focused || details.focused {
		t.Fatalf("initial focus table=%t details=%t", table.focused, details.focused)
	}
	page.Update(tea.KeyMsg{Type: tea.KeyDown})
	if table.keyEvents != 1 || details.keyEvents != 0 {
		t.Fatalf("table-focused routing table=%d details=%d", table.keyEvents, details.keyEvents)
	}

	page.Update(tea.KeyMsg{Type: tea.KeyTab})
	if table.focused || !details.focused {
		t.Fatalf("tab focus table=%t details=%t", table.focused, details.focused)
	}
	page.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if table.keyEvents != 1 || details.keyEvents != 1 {
		t.Fatalf("details-focused routing table=%d details=%d", table.keyEvents, details.keyEvents)
	}

	page.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if !table.focused || details.focused {
		t.Fatalf("shift-tab focus table=%t details=%t", table.focused, details.focused)
	}
}
