package dialog

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestSessionDialogSanitizesTitles(t *testing.T) {
	dialog := NewSessionDialogCmp().(*sessionDialogCmp)
	dialog.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	dialog.SetSessions([]session.Session{{
		ID:    "unsafe",
		Title: "Safe\x1b]0;spoofed\a\x1b[2J\u202e Workflow\nTitle\tHere",
	}})

	view := dialog.View()
	assertNoInjectedTerminalControls(t, view)
	plain := ansi.Strip(view)
	if strings.Contains(plain, "spoofed") {
		t.Fatalf("session title retained terminal-sequence payload: %q", plain)
	}
	if !strings.Contains(plain, "Safe Workflow Title Here") {
		t.Fatalf("sanitized session title is not visible: %q", plain)
	}
}

func TestSessionDialogFitsTinyAndCommonTerminals(t *testing.T) {
	sessions := []session.Session{
		{ID: "one", Title: "界界界界界界界界界界界界"},
		{ID: "two", Title: "A deliberately long session title that must be truncated"},
	}
	for _, size := range []tea.WindowSizeMsg{
		{Width: 1, Height: 1},
		{Width: 2, Height: 2},
		{Width: 3, Height: 3},
		{Width: 6, Height: 3},
		{Width: 12, Height: 4},
		{Width: 20, Height: 6},
		{Width: 40, Height: 10},
		{Width: 80, Height: 24},
	} {
		dialog := NewSessionDialogCmp().(*sessionDialogCmp)
		dialog.SetSessions(sessions)
		dialog.Update(size)
		view := dialog.View()
		if got := lipgloss.Width(view); got > size.Width {
			t.Errorf("dialog width at %dx%d = %d", size.Width, size.Height, got)
		}
		if got := lipgloss.Height(view); got > size.Height {
			t.Errorf("dialog height at %dx%d = %d: %q", size.Width, size.Height, got, ansi.Strip(view))
		}
	}
}

func TestSessionDialogVisibleRowsFollowTerminalHeight(t *testing.T) {
	sessions := make([]session.Session, 12)
	for i := range sessions {
		sessions[i] = session.Session{ID: fmt.Sprintf("session-%02d", i), Title: fmt.Sprintf("Session %02d", i)}
	}

	renderCount := func(height int) int {
		dialog := NewSessionDialogCmp().(*sessionDialogCmp)
		dialog.SetSessions(sessions)
		dialog.Update(tea.WindowSizeMsg{Width: 50, Height: height})
		return strings.Count(ansi.Strip(dialog.View()), "Session 0")
	}
	smallCount := renderCount(7)
	tallCount := renderCount(16)
	if smallCount == 0 {
		t.Fatal("compact dialog rendered no session rows")
	}
	if tallCount <= smallCount {
		t.Fatalf("visible rows did not grow with height: small=%d tall=%d", smallCount, tallCount)
	}
}

func TestSessionDialogMarksCurrentSessionIndependentlyOfCursor(t *testing.T) {
	dialog := NewSessionDialogCmp().(*sessionDialogCmp)
	dialog.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	dialog.SetSessions([]session.Session{
		{ID: "one", Title: "First"},
		{ID: "current", Title: "Current"},
		{ID: "three", Title: "Third"},
	})
	dialog.SetSelectedSession("current")
	dialog.Update(tea.KeyMsg{Type: tea.KeyDown})

	plain := ansi.Strip(dialog.View())
	if strings.Count(plain, "●") != 1 || !strings.Contains(plain, "● Current") {
		t.Fatalf("current-session marker missing or ambiguous: %q", plain)
	}
	if !strings.Contains(plain, "›   Third") {
		t.Fatalf("navigation cursor did not move independently: %q", plain)
	}
}

func TestSessionDialogKeepsSelectedRowVisibleAtCompactHeight(t *testing.T) {
	sessions := make([]session.Session, 10)
	for i := range sessions {
		sessions[i] = session.Session{ID: fmt.Sprintf("session-%d", i), Title: fmt.Sprintf("Item %d", i)}
	}
	dialog := NewSessionDialogCmp().(*sessionDialogCmp)
	dialog.SetSessions(sessions)
	dialog.selectedIdx = len(sessions) - 1
	dialog.Update(tea.WindowSizeMsg{Width: 30, Height: 5})

	plain := ansi.Strip(dialog.View())
	if !strings.Contains(plain, "Item 9") {
		t.Fatalf("selected row scrolled out of compact view: %q", plain)
	}
}
