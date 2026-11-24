package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
)

func TestThemeChangePreservesEditorCursorAndFocus(t *testing.T) {
	for _, focused := range []bool{true, false} {
		input := CreateTextArea(nil)
		input.SetWidth(20)
		input.SetHeight(3)
		input.SetValue("first line\nUnicode 界 cursor and trailing text")
		input.SetCursor(10)
		if !focused {
			input.Blur()
		}
		line, position := input.Line(), input.LineInfo()
		editor := &editorCmp{textarea: input}
		editor.Update(dialog.ThemeChangedMsg{})
		if got := editor.textarea.LineInfo(); got != position || editor.textarea.Line() != line {
			t.Fatalf("theme change moved cursor from line %d %#v to line %d %#v", line, position, editor.textarea.Line(), got)
		}
		if editor.textarea.Focused() != focused {
			t.Fatalf("theme change altered focus: got %t, want %t", editor.textarea.Focused(), focused)
		}
		if focused {
			editor.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
			if got := editor.textarea.Value(); got != "first line\nUnicode 界 Xcursor and trailing text" {
				t.Fatalf("typing after theme change edited the wrong location: %q", got)
			}
		}
	}
}

func TestCompletionReplacesTokenAtCursorAndPreservesSuffix(t *testing.T) {
	for _, cursor := range []int{5, 7} {
		input := CreateTextArea(nil)
		input.SetWidth(12)
		input.SetValue("Keep @doc unchanged\n参照 @doc and suffix\nlast line")
		input.CursorStart()
		input.CursorUp()
		input.SetCursor(cursor)
		editor := &editorCmp{textarea: input}
		editor.Update(dialog.CompletionSelectedMsg{SearchString: "@doc", CompletionValue: "資料/file.go"})
		if got, want := editor.textarea.Value(), "Keep @doc unchanged\n参照 資料/file.go and suffix\nlast line"; got != want {
			t.Fatalf("completion at column %d changed wrong token: got %q, want %q", cursor, got, want)
		}
		editor.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
		if got, want := editor.textarea.Value(), "Keep @doc unchanged\n参照 資料/file.goX and suffix\nlast line"; got != want {
			t.Fatalf("completion left cursor in wrong position: got %q, want %q", got, want)
		}
	}
}

func TestCompletionIgnoresTokenAwayFromCursor(t *testing.T) {
	input := CreateTextArea(nil)
	input.SetValue("Earlier @doc then a newer draft")
	editor := &editorCmp{textarea: input}
	editor.Update(dialog.CompletionSelectedMsg{SearchString: "@doc", CompletionValue: "file.go"})
	if got := editor.textarea.Value(); got != input.Value() {
		t.Fatalf("stale completion rewrote the draft: %q", got)
	}
}

func TestEditorSendRetainsCtrlSShortcut(t *testing.T) {
	found := false
	for _, shortcut := range editorMaps.Send.Keys() {
		if shortcut == "ctrl+s" {
			found = true
		}
	}
	if !found {
		t.Fatal("editor send no longer exposes ctrl+s")
	}
}

func TestDeleteAllAttachmentsHelpSpelling(t *testing.T) {
	if got := DeleteKeyMaps.DeleteAllAttachments.Help().Desc; got != "delete all attachments" {
		t.Fatalf("delete-all help = %q", got)
	}
}

func TestRejectedSendPreservesDraftAndAttachments(t *testing.T) {
	input := textarea.New()
	input.SetValue("carefully refactor the parser")
	attachment := message.Attachment{FilePath: "/tmp/example.png", FileName: "example.png", MimeType: "image/png", Content: []byte("image")}
	editor := &editorCmp{
		width:              100,
		height:             5,
		textarea:           input,
		attachments:        []message.Attachment{attachment},
		sendPending:        true,
		pendingText:        input.Value(),
		pendingAttachments: []message.Attachment{attachment},
	}

	editor.applySendResult(SendResultMsg{Text: input.Value(), Attachments: []message.Attachment{attachment}, Err: errors.New("provider unavailable")})

	if got := editor.textarea.Value(); got != "carefully refactor the parser" {
		t.Fatalf("draft = %q; want preserved draft", got)
	}
	if len(editor.attachments) != 1 || editor.attachments[0].FilePath != attachment.FilePath {
		t.Fatalf("attachments = %#v; want submitted attachment preserved", editor.attachments)
	}
	if !strings.Contains(editor.View(), "provider unavailable") || !strings.Contains(editor.View(), "draft preserved") {
		t.Fatalf("rejection is not rendered durably: %q", editor.View())
	}
}

func TestAcceptedSendClearsOnlySubmittedSnapshot(t *testing.T) {
	input := textarea.New()
	input.SetValue("first draft")
	attachment := message.Attachment{FilePath: "/tmp/example.png", Content: []byte("image")}
	editor := &editorCmp{
		textarea:           input,
		attachments:        []message.Attachment{attachment},
		sendPending:        true,
		pendingText:        "first draft",
		pendingAttachments: cloneAttachments([]message.Attachment{attachment}),
	}

	editor.applySendResult(SendResultMsg{Text: "first draft", Attachments: []message.Attachment{attachment}, Accepted: true})
	if got := editor.textarea.Value(); got != "" {
		t.Fatalf("accepted draft = %q; want cleared", got)
	}
	if len(editor.attachments) != 0 {
		t.Fatalf("accepted attachments = %#v; want cleared", editor.attachments)
	}

	editor.textarea.SetValue("new edits made while sending")
	editor.attachments = []message.Attachment{attachment, {FilePath: "/tmp/new.png"}}
	editor.sendPending = true
	editor.pendingText = "first draft"
	editor.pendingAttachments = cloneAttachments([]message.Attachment{attachment})
	editor.applySendResult(SendResultMsg{Text: "first draft", Attachments: []message.Attachment{attachment}, Accepted: true})
	if got := editor.textarea.Value(); got != "new edits made while sending" {
		t.Fatalf("new draft = %q; want edits preserved", got)
	}
	if len(editor.attachments) != 2 {
		t.Fatalf("new attachments = %#v; want edits preserved", editor.attachments)
	}
}
