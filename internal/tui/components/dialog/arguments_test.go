package dialog

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestArgumentsDialogWithNoFieldsHandlesMessages(t *testing.T) {
	dialog := NewMultiArgumentsDialogCmp("empty", "content", nil)
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 24, Height: 8},
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyShiftTab},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")},
	} {
		updated, _ := dialog.Update(msg)
		dialog = updated.(MultiArgumentsDialogCmp)
	}
	for _, key := range []tea.KeyType{tea.KeyEnter, tea.KeyEsc} {
		_, cmd := dialog.Update(tea.KeyMsg{Type: key})
		if cmd == nil {
			t.Fatalf("key %v did not close empty form", key)
		}
		result, ok := cmd().(CloseMultiArgumentsDialogMsg)
		if !ok || result.Submit != (key == tea.KeyEnter) || result.CommandID != "empty" || len(result.Args) != 0 {
			t.Fatalf("empty form result = %#v", result)
		}
	}
}

func TestArgumentsDialogFitsTerminalAndKeepsFocusedFieldVisible(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{
		{Width: 1, Height: 1},
		{Width: 20, Height: 6},
		{Width: 40, Height: 10},
		{Width: 80, Height: 24},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			names := make([]string, 12)
			for i := range names {
				names[i] = fmt.Sprintf("ARG_%02d", i)
			}
			dialog := NewMultiArgumentsDialogCmp("test", "content", names)
			dialog.SetSize(size.Width, size.Height)
			for i := range names {
				updated, _ := dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("value")})
				dialog = updated.(MultiArgumentsDialogCmp)
				view := dialog.View()
				if got := lipgloss.Width(view); got > size.Width {
					t.Fatalf("field %d: width = %d, want <= %d", i, got, size.Width)
				}
				if got := lipgloss.Height(view); got > size.Height {
					t.Fatalf("field %d: height = %d, want <= %d", i, got, size.Height)
				}
				if size.Width >= 20 && !strings.Contains(ansi.Strip(view), names[i]) {
					t.Fatalf("focused field %q is hidden: %q", names[i], ansi.Strip(view))
				}
				updated, _ = dialog.Update(tea.KeyMsg{Type: tea.KeyTab})
				dialog = updated.(MultiArgumentsDialogCmp)
			}
		})
	}
}

func TestArgumentsDialogSubmitsValuesAfterFocusNavigation(t *testing.T) {
	dialog := NewMultiArgumentsDialogCmp("test", "$FIRST $SECOND", []string{"FIRST", "SECOND"})
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("first value")},
		{Type: tea.KeyTab},
		{Type: tea.KeyRunes, Runes: []rune("second value")},
		{Type: tea.KeyShiftTab},
		{Type: tea.KeyEnter},
	} {
		updated, _ := dialog.Update(msg)
		dialog = updated.(MultiArgumentsDialogCmp)
	}
	_, cmd := dialog.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("confirming final field did not submit")
	}
	result, ok := cmd().(CloseMultiArgumentsDialogMsg)
	if !ok || !result.Submit || result.CommandID != "test" || result.Args["FIRST"] != "first value" || result.Args["SECOND"] != "second value" {
		t.Fatalf("submitted arguments = %#v", result)
	}
}
