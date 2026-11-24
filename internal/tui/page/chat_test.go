package page

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	appmodel "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
)

type stubChatAgent struct {
	runErr         error
	result         agent.AgentEvent
	runs           int
	runText        string
	runAttachments []message.Attachment
}

func (s *stubChatAgent) Subscribe(context.Context) <-chan pubsub.Event[agent.AgentEvent] {
	ch := make(chan pubsub.Event[agent.AgentEvent])
	close(ch)
	return ch
}
func (s *stubChatAgent) Model() models.Model { return models.Model{} }
func (s *stubChatAgent) Run(_ context.Context, _ string, text string, attachments ...message.Attachment) (<-chan agent.AgentEvent, error) {
	s.runs++
	s.runText = text
	s.runAttachments = append([]message.Attachment(nil), attachments...)
	if s.runErr != nil {
		return nil, s.runErr
	}
	ch := make(chan agent.AgentEvent, 1)
	ch <- s.result
	close(ch)
	return ch, nil
}

type recordingSplitPane struct {
	keyUpdates int
}

func (m *recordingSplitPane) Init() tea.Cmd { return nil }
func (m *recordingSplitPane) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		m.keyUpdates++
	}
	return m, nil
}
func (m *recordingSplitPane) View() string                            { return "" }
func (m *recordingSplitPane) SetSize(int, int) tea.Cmd                { return nil }
func (m *recordingSplitPane) GetSize() (int, int)                     { return 0, 0 }
func (m *recordingSplitPane) BindingKeys() []key.Binding              { return nil }
func (m *recordingSplitPane) SetLeftPanel(layout.Container) tea.Cmd   { return nil }
func (m *recordingSplitPane) SetRightPanel(layout.Container) tea.Cmd  { return nil }
func (m *recordingSplitPane) SetBottomPanel(layout.Container) tea.Cmd { return nil }
func (m *recordingSplitPane) ClearLeftPanel() tea.Cmd                 { return nil }
func (m *recordingSplitPane) ClearRightPanel() tea.Cmd                { return nil }
func (m *recordingSplitPane) ClearBottomPanel() tea.Cmd               { return nil }

type consumingCompletionDialog struct{}

func (m *consumingCompletionDialog) Init() tea.Cmd { return nil }
func (m *consumingCompletionDialog) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}
func (m *consumingCompletionDialog) View() string               { return "" }
func (m *consumingCompletionDialog) BindingKeys() []key.Binding { return nil }
func (m *consumingCompletionDialog) SetWidth(int)               {}
func (m *consumingCompletionDialog) ConsumesKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "down", "tab", "enter":
		return true
	default:
		return false
	}
}
func (s *stubChatAgent) Cancel(string)                           {}
func (s *stubChatAgent) IsSessionBusy(string) bool               { return false }
func (s *stubChatAgent) IsBusy() bool                            { return false }
func (s *stubChatAgent) Summarize(context.Context, string) error { return nil }
func (s *stubChatAgent) Update(config.AgentName, models.ModelID) (models.Model, error) {
	return models.Model{}, nil
}

func TestTaskStatusForCommand(t *testing.T) {
	tests := map[string]engineering.Status{
		"retry":  engineering.StatusRetrying,
		"pause":  engineering.StatusPaused,
		"resume": engineering.StatusReady,
		"cancel": engineering.StatusCanceled,
	}
	for command, expected := range tests {
		got, ok := taskStatusForCommand(command)
		if !ok || got != expected {
			t.Fatalf("task command %q = %q, %t; want %q, true", command, got, ok, expected)
		}
	}
	if _, ok := taskStatusForCommand("complete"); ok {
		t.Fatal("task command unexpectedly accepted unsupported completion transition")
	}
}

func TestAgentStatusForCommand(t *testing.T) {
	tests := map[string]orchestration.AgentStatus{
		"start":    orchestration.AgentStarting,
		"retry":    orchestration.AgentStarting,
		"run":      orchestration.AgentRunning,
		"pause":    orchestration.AgentPaused,
		"resume":   orchestration.AgentResumed,
		"cancel":   orchestration.AgentCanceled,
		"complete": orchestration.AgentCompleted,
		"fail":     orchestration.AgentFailed,
	}
	for command, expected := range tests {
		got, ok := agentStatusForCommand(command)
		if !ok || got != expected {
			t.Fatalf("agent command %q = %q, %t; want %q, true", command, got, ok, expected)
		}
	}
	if _, ok := agentStatusForCommand("delete"); ok {
		t.Fatal("agent command unexpectedly accepted unsupported transition")
	}
}

func TestSendMessageAcknowledgesAcceptedAndRejectedRuns(t *testing.T) {
	tests := []struct {
		name     string
		runErr   error
		accepted bool
	}{
		{name: "accepted", accepted: true},
		{name: "rejected", runErr: errors.New("provider unavailable")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page := &chatPage{
				app:        &appmodel.App{CoderAgent: &stubChatAgent{runErr: tc.runErr}},
				session:    session.Session{ID: "session-1"},
				runNotices: make(map[string]runNotice),
			}
			_, cmd := page.Update(chat.SendMsg{Text: "fix the tests"})
			messages := executeCommands(cmd)
			var result *chat.SendResultMsg
			for _, msg := range messages {
				if typed, ok := msg.(chat.SendResultMsg); ok {
					copy := typed
					result = &copy
				}
			}
			if result == nil {
				t.Fatal("send did not emit an editor acknowledgement")
			}
			if result.Accepted != tc.accepted {
				t.Fatalf("accepted = %t; want %t (err=%v)", result.Accepted, tc.accepted, result.Err)
			}
		})
	}
}

func TestAgentSlashCommandPreservesAttachments(t *testing.T) {
	agent := &stubChatAgent{}
	page := &chatPage{
		app:        &appmodel.App{CoderAgent: agent},
		session:    session.Session{ID: "session-1"},
		runNotices: make(map[string]runNotice),
	}
	attachment := message.Attachment{FilePath: "/tmp/screenshot.png", Content: []byte("image")}

	if _, err := page.sendMessage("/test ./internal/tui", []message.Attachment{attachment}); err != nil {
		t.Fatal(err)
	}
	if agent.runText != "Execute the RSCH workflow command /test with arguments: ./internal/tui" {
		t.Fatalf("run text = %q", agent.runText)
	}
	if len(agent.runAttachments) != 1 || agent.runAttachments[0].FilePath != attachment.FilePath {
		t.Fatalf("run attachments = %#v; want attachment preserved", agent.runAttachments)
	}
}

func TestHelpSlashCommandRequestsOverlayWithoutAgentRun(t *testing.T) {
	agent := &stubChatAgent{}
	page := &chatPage{
		app:        &appmodel.App{CoderAgent: agent},
		session:    session.Session{ID: "session-1"},
		runNotices: make(map[string]runNotice),
	}

	_, cmd := page.Update(chat.SendMsg{Text: "/help"})
	messages := executeCommands(cmd)
	foundHelp := false
	for _, msg := range messages {
		if _, ok := msg.(dialog.ShowHelpDialogMsg); ok {
			foundHelp = true
		}
	}
	if !foundHelp {
		t.Fatalf("/help messages = %#v; want ShowHelpDialogMsg", messages)
	}
	if agent.runs != 0 {
		t.Fatalf("agent runs = %d; want 0", agent.runs)
	}
}

func TestUnknownSlashCommandIsRejectedBeforeAgentRun(t *testing.T) {
	agent := &stubChatAgent{}
	page := &chatPage{
		app:        &appmodel.App{CoderAgent: agent},
		session:    session.Session{ID: "session-1"},
		runNotices: make(map[string]runNotice),
	}

	_, cmd := page.Update(chat.SendMsg{Text: "/hlep"})
	messages := executeCommands(cmd)
	var result *chat.SendResultMsg
	for _, msg := range messages {
		if typed, ok := msg.(chat.SendResultMsg); ok {
			copy := typed
			result = &copy
		}
	}
	if result == nil || result.Accepted || result.Err == nil {
		t.Fatalf("send result = %#v; want rejected command", result)
	}
	if got, want := result.Err.Error(), `unknown slash command "/hlep"; press / to browse commands`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if agent.runs != 0 {
		t.Fatalf("agent runs = %d; want 0", agent.runs)
	}
}

func TestCompletionNavigationDoesNotReachComposerLayout(t *testing.T) {
	layoutModel := &recordingSplitPane{}
	page := &chatPage{
		showCompletionDialog: true,
		completionDialog:     &consumingCompletionDialog{},
		layout:               layoutModel,
	}

	updated, _ := page.Update(tea.KeyMsg{Type: tea.KeyDown})
	page = updated.(*chatPage)
	if layoutModel.keyUpdates != 0 {
		t.Fatalf("completion navigation reached composer layout %d times", layoutModel.keyUpdates)
	}

	updated, _ = page.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	_ = updated.(*chatPage)
	if layoutModel.keyUpdates != 1 {
		t.Fatalf("completion query key updates = %d; want composer kept in sync", layoutModel.keyUpdates)
	}
}

func TestRunFailureAndCancellationRemainVisible(t *testing.T) {
	page := &chatPage{session: session.Session{ID: "session-1"}, runNotices: make(map[string]runNotice)}
	page.applyRunResult(runFinishedMsg{sessionID: "session-1", err: errors.New("stream disconnected")})
	if notice := page.runNotices["session-1"]; notice.message != "stream disconnected" || notice.canceled {
		t.Fatalf("failure notice = %#v", notice)
	}

	page.applyRunResult(runFinishedMsg{sessionID: "session-1", err: agent.ErrRequestCancelled})
	if notice := page.runNotices["session-1"]; !notice.canceled {
		t.Fatalf("cancellation notice = %#v; want canceled", notice)
	}

	page.applyRunResult(runFinishedMsg{sessionID: "session-1"})
	if _, ok := page.runNotices["session-1"]; ok {
		t.Fatal("successful run did not clear the prior failure notice")
	}
}

type recordingSidebar struct {
	historyEvents int
}

func (*recordingSidebar) Init() tea.Cmd { return nil }
func (m *recordingSidebar) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(pubsub.Event[history.File]); ok {
		m.historyEvents++
	}
	return m, nil
}
func (*recordingSidebar) View() string { return "previous session" }

func TestNewSessionKeepsPreviousSidebarHiddenAfterResize(t *testing.T) {
	sidebar := &recordingSidebar{}
	container := layout.NewContainer(sidebar)
	layoutModel := layout.NewSplitPane(layout.WithRightPanel(container))
	page := &chatPage{
		width:              160,
		session:            session.Session{ID: "previous-session"},
		sidebar:            container,
		sidebarInitialized: true,
		layout:             layoutModel,
	}
	page.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true})
	for _, width := range []int{160, 80, 160} {
		page.SetSize(width, 40)
		if strings.Contains(layoutModel.View(), "previous session") {
			t.Fatalf("previous session sidebar reappeared at width %d", width)
		}
	}
	// The hidden sidebar still owns its subscription and must consume events
	// so that receiving the next event is scheduled while no session is open.
	page.Update(pubsub.Event[history.File]{Type: pubsub.UpdatedEvent})
	if sidebar.historyEvents != 1 {
		t.Fatalf("hidden sidebar received %d history events, want 1", sidebar.historyEvents)
	}
	page.Update(chat.SessionSelectedMsg(session.Session{ID: "next-session"}))
	if !strings.Contains(layoutModel.View(), "previous session") {
		t.Fatal("selecting the next session did not restore the sidebar")
	}
}

func TestChatHeaderTracksCurrentSessionUpdates(t *testing.T) {
	page := &chatPage{
		width:   160,
		session: session.Session{ID: "current", Title: "Old title"},
		layout:  &recordingSplitPane{},
	}
	page.Update(pubsub.Event[session.Session]{
		Type:    pubsub.UpdatedEvent,
		Payload: session.Session{ID: "current", Title: "Updated title", MessageCount: 9},
	})
	if view := page.View(); !strings.Contains(view, "Updated title") {
		t.Fatalf("session title did not update in header:\n%s", view)
	}
	page.Update(pubsub.Event[session.Session]{
		Type:    pubsub.UpdatedEvent,
		Payload: session.Session{ID: "other", Title: "Other title", MessageCount: 2},
	})
	page.SetSize(100, 24)
	if view := page.View(); !strings.Contains(view, "session 9 msg") {
		t.Fatalf("compact header lost current session count:\n%s", view)
	}
}

func TestExpandAgentSlashCommand(t *testing.T) {
	prompt, ok := expandAgentSlashCommand("/test ./internal/tui")
	if !ok || prompt != "Execute the RSCH workflow command /test with arguments: ./internal/tui" {
		t.Fatalf("prompt = %q, %t", prompt, ok)
	}
	if _, ok := expandAgentSlashCommand("/status"); ok {
		t.Fatal("local slash command was classified as an agent run")
	}
}

func executeCommands(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var messages []tea.Msg
		for _, child := range batch {
			messages = append(messages, executeCommands(child)...)
		}
		return messages
	}
	return []tea.Msg{msg}
}
