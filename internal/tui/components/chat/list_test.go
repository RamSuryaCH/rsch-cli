package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

func transcriptFixture(count int) []message.Message {
	messages := make([]message.Message, 0, count)
	for i := 0; i < count; i++ {
		messages = append(messages, message.Message{
			ID:        fmt.Sprintf("message-%d", i),
			SessionID: "session-1",
			Role:      message.User,
			Parts:     []message.ContentPart{message.TextContent{Text: fmt.Sprintf("line %d\nmore detail", i)}},
		})
	}
	return messages
}

func TestStreamingPreservesReaderPositionAwayFromBottom(t *testing.T) {
	model := NewMessagesCmp(nil).(*messagesCmp)
	model.session = session.Session{ID: "session-1"}
	model.messages = transcriptFixture(20)
	model.currentMsgID = model.messages[len(model.messages)-1].ID
	model.SetSize(60, 10)
	model.viewport.GotoTop()

	updated := model.messages[len(model.messages)-1]
	updated.Parts = []message.ContentPart{message.TextContent{Text: "updated\nstreaming\ncontent"}}
	model.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: updated})

	if model.viewport.YOffset != 0 {
		t.Fatalf("streaming moved reader from top to offset %d", model.viewport.YOffset)
	}
	if !model.newOutput {
		t.Fatal("streaming away from bottom did not expose the new-output indicator")
	}
}

func TestStreamingContinuesFollowingWhenAlreadyAtBottom(t *testing.T) {
	model := NewMessagesCmp(nil).(*messagesCmp)
	model.session = session.Session{ID: "session-1"}
	model.messages = transcriptFixture(20)
	model.currentMsgID = model.messages[len(model.messages)-1].ID
	model.SetSize(60, 10)
	model.viewport.GotoBottom()

	updated := model.messages[len(model.messages)-1]
	updated.Parts = []message.ContentPart{message.TextContent{Text: "updated\nstreaming\ncontent\nwith another row"}}
	model.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: updated})

	if !model.viewport.AtBottom() {
		t.Fatal("streaming stopped following while reader was already at bottom")
	}
	if model.newOutput {
		t.Fatal("bottom-following stream incorrectly showed new-output indicator")
	}
}

func TestEmptyTranscriptHasOneClearCTAAndLabeledExamples(t *testing.T) {
	model := NewMessagesCmp(nil).(*messagesCmp)
	model.SetSize(72, 9)

	view := ansi.Strip(model.View())
	for _, expected := range []string{
		"Start with a request",
		"Type a request in the composer below.",
		"Examples",
		"Inspect:",
		"Change:",
		"Verify:",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("empty transcript is missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, "›") {
		t.Fatalf("starter examples look selectable:\n%s", view)
	}
	if strings.Contains(view, "⌬ RSCH") {
		t.Fatalf("empty transcript repeats workspace identity:\n%s", view)
	}
}

func TestIdleTranscriptDoesNotDuplicateRootShortcuts(t *testing.T) {
	model := NewMessagesCmp(nil).(*messagesCmp)
	model.messages = transcriptFixture(1)
	model.currentMsgID = model.messages[0].ID
	model.SetSize(60, 8)
	if activity := model.activity(); activity != "" {
		t.Fatalf("idle transcript rendered an activity footer: %q", ansi.Strip(activity))
	}

	view := ansi.Strip(model.View())
	for _, duplicate := range []string{"enter send", "ctrl+j", "alt+p", "f1 help"} {
		if strings.Contains(view, duplicate) {
			t.Fatalf("transcript repeats root shortcut %q:\n%s", duplicate, view)
		}
	}
}

func TestTranscriptKeepsCompactNewOutputRow(t *testing.T) {
	model := NewMessagesCmp(nil).(*messagesCmp)
	model.messages = transcriptFixture(4)
	model.currentMsgID = model.messages[len(model.messages)-1].ID
	model.newOutput = true
	model.SetSize(36, 6)

	view := model.View()
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "new output") || !strings.Contains(plain, "pgdn follow") {
		t.Fatalf("new-output row is not actionable:\n%s", plain)
	}
	if lipgloss.Height(view) > 6 {
		t.Fatalf("new-output transcript height = %d, want <= 6", lipgloss.Height(view))
	}
}

func TestTranscriptViewNeverExceedsSmallAllocation(t *testing.T) {
	for height := 0; height <= 8; height++ {
		states := map[string]*messagesCmp{
			"empty":      NewMessagesCmp(nil).(*messagesCmp),
			"loading":    NewMessagesCmp(nil).(*messagesCmp),
			"new-output": NewMessagesCmp(nil).(*messagesCmp),
		}
		states["loading"].rendering = true
		states["new-output"].messages = transcriptFixture(8)
		states["new-output"].currentMsgID = states["new-output"].messages[len(states["new-output"].messages)-1].ID
		states["new-output"].newOutput = true

		for name, model := range states {
			model.SetSize(32, height)
			view := model.View()
			if height == 0 {
				if view != "" {
					t.Fatalf("%s height 0 rendered %q", name, view)
				}
				continue
			}
			if got := lipgloss.Height(view); got > height {
				t.Fatalf("%s height %d rendered %d rows:\n%s", name, height, got, ansi.Strip(view))
			}
			if got := lipgloss.Width(view); got > 32 {
				t.Fatalf("%s height %d rendered width %d:\n%s", name, height, got, ansi.Strip(view))
			}
		}
	}
}
