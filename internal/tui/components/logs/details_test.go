package logs

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

func TestDetailsNewSelectionStartsAtTop(t *testing.T) {
	details := NewLogsDetails().(*detailCmp)
	details.SetSize(40, 6)
	first := longLogMessage()
	details.Update(selectedLogMsg(first))
	details.viewport.GotoBottom()
	second := first
	second.ID = "log-2"
	second.Message = "second log header\n" + first.Message
	details.Update(selectedLogMsg(second))
	if details.viewport.YOffset != 0 {
		t.Fatalf("new selection retained scroll offset %d", details.viewport.YOffset)
	}
}

func TestDetailsLongLinesRemainReachableAndFitWidth(t *testing.T) {
	details := NewLogsDetails().(*detailCmp)
	details.SetSize(12, 6)
	details.Update(selectedLogMsg(logging.LogMessage{
		ID: "long", Message: strings.Repeat("a", 60) + "TAIL", Level: "info",
	}))
	details.viewport.GotoBottom()
	view := details.View()
	if !strings.Contains(ansi.Strip(view), "TAIL") {
		t.Fatalf("long log tail is unreachable: %q", ansi.Strip(view))
	}
	if got := lipgloss.Width(view); got > 12 {
		t.Fatalf("details width = %d, want <= 12", got)
	}
	details.SetSize(0, 6)
	if view := details.View(); view != "" {
		t.Fatalf("zero-width details rendered %q", view)
	}
}

func longLogMessage() logging.LogMessage {
	lines := make([]string, 40)
	for idx := range lines {
		lines[idx] = "detail line"
	}
	return logging.LogMessage{
		ID:      "log-1",
		Time:    time.Unix(1, 0).UTC(),
		Level:   "info",
		Message: strings.Join(lines, "\n"),
	}
}

func TestDetailsKeyboardScrollingRequiresFocus(t *testing.T) {
	details := NewLogsDetails().(*detailCmp)
	details.currentLog = longLogMessage()
	details.SetSize(40, 6)
	details.SetFocused(false)

	details.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if details.viewport.YOffset != 0 {
		t.Fatalf("blurred details scrolled to %d", details.viewport.YOffset)
	}

	details.SetFocused(true)
	details.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if details.viewport.YOffset == 0 {
		t.Fatal("focused details did not page down")
	}

	details.Update(tea.KeyMsg{Type: tea.KeyHome})
	if details.viewport.YOffset != 0 {
		t.Fatalf("home offset = %d, want 0", details.viewport.YOffset)
	}

	details.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !details.viewport.AtBottom() {
		t.Fatal("end did not move details to the bottom")
	}

	details.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if details.viewport.AtBottom() {
		t.Fatal("k did not scroll details up from the bottom")
	}
	details.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if details.viewport.YOffset == 0 {
		t.Fatal("j did not scroll details down")
	}
}

func TestDetailsFocusIndicatorDoesNotDependOnColor(t *testing.T) {
	details := NewLogsDetails().(*detailCmp)
	details.SetSize(40, 4)
	details.SetFocused(true)

	view := ansi.Strip(details.View())
	want := styles.SelectionMarker(true) + "Log details"
	if !strings.Contains(view, want) {
		t.Fatalf("focused details view = %q, want visible marker %q", view, want)
	}
}

func TestDetailsSanitizesUntrustedLogText(t *testing.T) {
	details := NewLogsDetails().(*detailCmp)
	details.currentLog = logging.LogMessage{
		ID:      "log-1",
		Time:    time.Unix(1, 0).UTC(),
		Level:   "info\u202e",
		Message: "message\x1b]52;c;clipboard\a\u202e",
		Attributes: []logging.Attr{{
			Key:   "key\x1b]0;title\a",
			Value: "value\u2066",
		}},
	}
	details.SetSize(80, 12)

	view := details.View()
	for _, unsafe := range []string{"\x1b]52", "\x1b]0", "\u202e", "\u2066"} {
		if strings.Contains(view, unsafe) {
			t.Fatalf("details view retained unsafe sequence %q", unsafe)
		}
	}
	if !strings.Contains(ansi.Strip(view), "message") {
		t.Fatalf("sanitization removed safe message content: %q", ansi.Strip(view))
	}
}
