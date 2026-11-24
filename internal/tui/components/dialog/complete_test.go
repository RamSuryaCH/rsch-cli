package dialog

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCompletionNavigationDoesNotMoveQueryCursor(t *testing.T) {
	for _, direction := range []tea.KeyType{tea.KeyUp, tea.KeyDown} {
		t.Run(tea.KeyMsg{Type: direction}.String(), func(t *testing.T) {
			provider := &recordingCompletionProvider{}
			dialog := NewCompletionDialogCmp(provider).(*completionDialogCmp)
			dialog.pseudoSearchTextArea.SetWidth(12)
			dialog.Update(completionRune('@'))
			query := strings.Repeat("directory/", 6)
			dialog.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(query)})
			dialog.Update(completionKey(direction))
			dialog.Update(completionRune('x'))
			if dialog.query != query+"x" {
				t.Fatalf("navigation moved query cursor: got %q, want %q", dialog.query, query+"x")
			}
		})
	}
}

type recordingCompletionProvider struct {
	queries []string
}

func (p *recordingCompletionProvider) GetId() string { return "recording" }
func (p *recordingCompletionProvider) GetEntry() CompletionItemI {
	return NewCompletionItem(CompletionItem{Title: "Files", Value: "files"})
}
func (p *recordingCompletionProvider) GetChildEntries(query string) ([]CompletionItemI, error) {
	p.queries = append(p.queries, query)
	return []CompletionItemI{
		NewCompletionItem(CompletionItem{Title: query, Value: query}),
	}, nil
}

func completionKey(keyType tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: keyType}
}

func completionRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestCompletionConsumesSelectionNavigationOnly(t *testing.T) {
	provider := &recordingCompletionProvider{}
	dialog := NewCompletionDialogCmp(provider).(*completionDialogCmp)

	for _, msg := range []tea.KeyMsg{
		completionKey(tea.KeyUp),
		completionKey(tea.KeyDown),
		completionKey(tea.KeyTab),
		completionKey(tea.KeyEnter),
	} {
		if !dialog.ConsumesKey(msg) {
			t.Errorf("completion does not consume %q", msg.String())
		}
	}
	for _, msg := range []tea.KeyMsg{completionRune('a'), completionKey(tea.KeyBackspace)} {
		if dialog.ConsumesKey(msg) {
			t.Errorf("completion unexpectedly consumes composer editing key %q", msg.String())
		}
	}
}

func TestCompletionCloseResetsQueryBeforeReopen(t *testing.T) {
	provider := &recordingCompletionProvider{}
	dialog := NewCompletionDialogCmp(provider).(*completionDialogCmp)

	dialog.Update(completionRune('@'))
	dialog.Update(completionRune('a'))
	if dialog.query != "a" {
		t.Fatalf("query before close = %q", dialog.query)
	}

	dialog.close()
	if dialog.query != "" || dialog.pseudoSearchTextArea.Focused() || len(dialog.listView.GetItems()) != 0 {
		t.Fatalf("completion state after close: query=%q focused=%t items=%d", dialog.query, dialog.pseudoSearchTextArea.Focused(), len(dialog.listView.GetItems()))
	}

	dialog.Update(completionRune('@'))
	dialog.Update(completionRune('a'))
	queryCalls := 0
	for _, query := range provider.queries {
		if query == "a" {
			queryCalls++
		}
	}
	if queryCalls != 2 {
		t.Fatalf("provider query calls for reopened completion = %d; want 2 (%v)", queryCalls, provider.queries)
	}
}
