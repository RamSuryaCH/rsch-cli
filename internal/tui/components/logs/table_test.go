package logs

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

func selectedLogFromCommand(t *testing.T, cmd tea.Cmd) logging.LogMessage {
	t.Helper()
	if cmd == nil {
		t.Fatal("log selection did not emit a details update")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if child != nil {
				if selected, ok := child().(selectedLogMsg); ok {
					return logging.LogMessage(selected)
				}
			}
		}
	}
	if selected, ok := msg.(selectedLogMsg); ok {
		return logging.LogMessage(selected)
	}
	t.Fatalf("command returned no log selection: %#v", msg)
	return logging.LogMessage{}
}

func TestLogTablePublishesInitialSelectionAndPreservesItOnArrival(t *testing.T) {
	entries := []logging.LogMessage{
		{ID: "oldest", Time: time.Unix(1, 0), Message: "oldest log"},
		{ID: "newest", Time: time.Unix(2, 0), Message: "newest log"},
	}
	table := NewLogsTable().(*tableCmp)
	table.listLogs = func() []logging.LogMessage { return append([]logging.LogMessage(nil), entries...) }
	table.SetSize(80, 6)
	selected := selectedLogFromCommand(t, table.Init())
	if selected.ID != "newest" {
		t.Fatalf("initial details selected %q, want newest", selected.ID)
	}

	_, cmd := table.Update(tea.KeyMsg{Type: tea.KeyDown})
	selected = selectedLogFromCommand(t, cmd)
	if selected.ID != "oldest" {
		t.Fatalf("navigation selected %q, want oldest", selected.ID)
	}
	entries = append(entries, logging.LogMessage{ID: "arrived", Time: time.Unix(3, 0)})
	table.Update(pubsub.Event[logging.LogMessage]{Type: pubsub.CreatedEvent, Payload: entries[2]})
	if row := table.table.SelectedRow(); row == nil || row[0] != "oldest" {
		t.Fatalf("new arrival displaced the inspected log: %#v", row)
	}
}

func TestLogTableFirstArrivalPublishesSelection(t *testing.T) {
	var entries []logging.LogMessage
	table := NewLogsTable().(*tableCmp)
	table.listLogs = func() []logging.LogMessage { return append([]logging.LogMessage(nil), entries...) }
	table.SetSize(80, 6)
	table.Init()
	entries = append(entries, logging.LogMessage{ID: "first", Time: time.Unix(1, 0)})
	_, cmd := table.Update(pubsub.Event[logging.LogMessage]{Type: pubsub.CreatedEvent, Payload: entries[0]})
	if selected := selectedLogFromCommand(t, cmd); selected.ID != "first" {
		t.Fatalf("first arrival selected %q", selected.ID)
	}
}

func TestTableFocusIndicatorDoesNotDependOnColor(t *testing.T) {
	table := NewLogsTable().(*tableCmp)
	if got, want := table.table.Columns()[0].Title, styles.SelectionLabel("ID", true); got != want {
		t.Fatalf("focused table title = %q, want %q", got, want)
	}

	table.SetFocused(false)
	if got, want := table.table.Columns()[0].Title, styles.SelectionLabel("ID", false); got != want {
		t.Fatalf("blurred table title = %q, want %q", got, want)
	}
	if table.IsFocused() || table.table.Focused() {
		t.Fatal("table remained focused after SetFocused(false)")
	}
}

func TestLogTableRowSanitizesAndFlattensUntrustedText(t *testing.T) {
	row := logTableRow(logging.LogMessage{
		ID:      "id\x1b]0;title\a",
		Time:    time.Unix(1, 0).UTC(),
		Level:   "warn\u202e",
		Message: "first\nsecond\x1b]52;c;clipboard\a",
		Attributes: []logging.Attr{{
			Key:   "key\u2066",
			Value: "value\x1b[2J",
		}},
	})

	joined := strings.Join(row, "|")
	for _, unsafe := range []string{"\x1b]", "\x1b[", "\u202e", "\u2066", "\n"} {
		if strings.Contains(joined, unsafe) {
			t.Fatalf("table row retained unsafe sequence %q in %q", unsafe, joined)
		}
	}
	if !strings.Contains(joined, "first second") {
		t.Fatalf("table row lost safe content: %q", joined)
	}
}
