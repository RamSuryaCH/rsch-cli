package dialog

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestFuzzyCommandMatch(t *testing.T) {
	for _, tc := range []struct {
		query, value string
		want         bool
	}{
		{"mdl", "switch model", true},
		{"git", "review git changes", true},
		{"xyz", "switch model", false},
		{"", "anything", true},
	} {
		if got := fuzzyCommandMatch(tc.query, tc.value); got != tc.want {
			t.Fatalf("fuzzyCommandMatch(%q, %q) = %t, want %t", tc.query, tc.value, got, tc.want)
		}
	}
}

func TestCommandDialogFitsCompactTerminal(t *testing.T) {
	dialog := NewCommandDialogCmp().(*commandDialogCmp)
	dialog.Init()
	dialog.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	dialog.SetCommands([]Command{{ID: "status", Title: "Show a very long status title"}})
	view := dialog.View()
	if got := lipgloss.Width(view); got > 20 {
		t.Fatalf("compact command dialog width = %d, want <= 20", got)
	}
	if got := lipgloss.Height(view); got > 10 {
		t.Fatalf("compact command dialog height = %d, want <= 10", got)
	}
}

func TestCommandSearchTypingJDoesNotMoveSelection(t *testing.T) {
	dialog := NewCommandDialogCmp().(*commandDialogCmp)
	dialog.Init()
	dialog.SetCommands([]Command{
		{ID: "jump-first", Title: "Jump first"},
		{ID: "jump-second", Title: "Jump second"},
	})

	dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	selected, idx := dialog.listView.GetSelectedItem()
	if dialog.search.Value() != "j" {
		t.Fatalf("search value = %q, want j", dialog.search.Value())
	}
	if idx != 0 || selected.ID != "jump-first" {
		t.Fatalf("typing in search moved selection to %d (%q)", idx, selected.ID)
	}
}

func TestCommandDialogInitResetsStaleSearch(t *testing.T) {
	dialog := NewCommandDialogCmp().(*commandDialogCmp)
	dialog.SetCommands([]Command{
		{ID: "status", Title: "Workflow Status"},
		{ID: "review", Title: "Review Changes"},
	})
	dialog.Init()
	dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz")})
	if got := len(dialog.listView.GetItems()); got != 0 {
		t.Fatalf("filtered commands = %d, want 0", got)
	}

	dialog.Init()
	if got := dialog.search.Value(); got != "" {
		t.Fatalf("search after reopen = %q, want empty", got)
	}
	if got := len(dialog.listView.GetItems()); got != 2 {
		t.Fatalf("commands after reopen = %d, want 2", got)
	}
}

func TestCommandSearchPreservesSelectionUntilQueryChanges(t *testing.T) {
	dialog := NewCommandDialogCmp().(*commandDialogCmp)
	dialog.SetCommands([]Command{
		{ID: "inspect-first", Title: "Inspect first"},
		{ID: "inspect-second", Title: "Inspect second"},
		{ID: "inspect-third", Title: "Inspect third"},
		{ID: "other", Title: "Other"},
	})
	dialog.Init()
	dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("inspect")})
	dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	selected, idx := dialog.listView.GetSelectedItem()
	if idx != 2 || selected.ID != "inspect-third" {
		t.Fatalf("filtered navigation selected %d (%q), want third match", idx, selected.ID)
	}

	dialog.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if _, idx := dialog.listView.GetSelectedItem(); idx != 2 {
		t.Fatalf("resize reset filtered selection to %d", idx)
	}
	dialog.Update(tea.KeyMsg{Type: tea.KeyUp})
	if _, idx := dialog.listView.GetSelectedItem(); idx != 1 {
		t.Fatalf("up selected %d, want second match", idx)
	}

	dialog.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := len(dialog.listView.GetItems()); got != 4 {
		t.Fatalf("clearing query left %d commands, want 4", got)
	}
	if _, idx := dialog.listView.GetSelectedItem(); idx != 0 {
		t.Fatalf("changed query retained selection %d, want first match", idx)
	}
}

func TestCommandDialogShowsTruthfulEmptyStates(t *testing.T) {
	dialog := NewCommandDialogCmp().(*commandDialogCmp)
	dialog.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	dialog.SetCommands([]Command{{ID: "status", Title: "Workflow Status"}})
	dialog.Init()
	dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz")})

	view := ansi.Strip(dialog.View())
	if !strings.Contains(view, `No matches for "zzz"`) {
		t.Fatalf("query-specific empty state missing from %q", view)
	}
	if strings.Contains(view, "No commands available") {
		t.Fatalf("no-match state incorrectly reports no commands: %q", view)
	}

	dialog.SetCommands(nil)
	dialog.Init()
	view = ansi.Strip(dialog.View())
	if !strings.Contains(view, "No commands available") {
		t.Fatalf("empty registry state missing from %q", view)
	}
}

func TestCommandDialogShowsIDsCountsAndOverflowPosition(t *testing.T) {
	dialog := NewCommandDialogCmp().(*commandDialogCmp)
	dialog.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	commands := make([]Command, 10)
	for i := range commands {
		commands[i] = Command{ID: fmt.Sprintf("command-%02d", i+1), Title: "Action"}
	}
	dialog.SetCommands(commands)
	dialog.Init()

	view := ansi.Strip(dialog.View())
	for _, want := range []string{"10 commands", "/command-01", "1/10"} {
		if !strings.Contains(view, want) {
			t.Fatalf("palette omitted %q: %q", want, view)
		}
	}

	dialog.Update(tea.KeyMsg{Type: tea.KeyDown})
	if view := ansi.Strip(dialog.View()); !strings.Contains(view, "2/10") {
		t.Fatalf("overflow position did not follow selection: %q", view)
	}
}

func TestCommandRenderSanitizesWorkspaceMetadata(t *testing.T) {
	command := Command{
		ID:          "status\x1b]0;spoofed\a",
		Title:       "Workflow\nStatus",
		Description: "safe\x1b[2J\u202edetail",
	}
	rendered := command.Render(false, 80)
	plain := ansi.Strip(rendered)

	if strings.ContainsAny(plain, "\x1b\a\n\r") || strings.Contains(plain, "\u202e") {
		t.Fatalf("rendered command retained unsafe controls: %q", plain)
	}
	if strings.Contains(plain, "spoofed") {
		t.Fatalf("rendered command retained terminal-sequence payload: %q", plain)
	}
	for _, want := range []string{"/status", "Workflow Status", "safedetail"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("sanitized command omitted %q: %q", want, plain)
		}
	}
}
