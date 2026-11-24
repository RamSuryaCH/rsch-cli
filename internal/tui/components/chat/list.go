package chat

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type cacheItem struct {
	width   int
	content []uiMessage
}
type messagesCmp struct {
	app           *app.App
	width, height int
	viewport      viewport.Model
	session       session.Session
	messages      []message.Message
	uiMessages    []uiMessage
	currentMsgID  string
	cachedContent map[string]cacheItem
	spinner       spinner.Model
	rendering     bool
	attachments   viewport.Model
	newOutput     bool
}
type sessionLoadedMsg struct {
	sessionID string
	messages  []message.Message
	err       error
}

type MessageKeys struct {
	PageDown     key.Binding
	PageUp       key.Binding
	HalfPageUp   key.Binding
	HalfPageDown key.Binding
}

var messageKeys = MessageKeys{
	PageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("f/pgdn", "page down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("b/pgup", "page up"),
	),
	HalfPageUp: key.NewBinding(
		key.WithKeys("alt+up"),
		key.WithHelp("alt+↑", "½ page up"),
	),
	HalfPageDown: key.NewBinding(
		key.WithKeys("alt+down"),
		key.WithHelp("alt+↓", "½ page down"),
	),
}

func (m *messagesCmp) Init() tea.Cmd {
	return tea.Batch(m.viewport.Init(), m.spinner.Tick)
}

func (m *messagesCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case dialog.ThemeChangedMsg:
		m.rerender()
		return m, nil
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			cmd := m.SetSession(msg)
			return m, cmd
		}
		return m, nil
	case SessionClearedMsg:
		m.session = session.Session{}
		m.messages = make([]message.Message, 0)
		m.currentMsgID = ""
		m.rendering = false
		m.newOutput = false
		return m, nil

	case tea.KeyMsg:
		if key.Matches(msg, messageKeys.PageUp) || key.Matches(msg, messageKeys.PageDown) ||
			key.Matches(msg, messageKeys.HalfPageUp) || key.Matches(msg, messageKeys.HalfPageDown) {
			u, cmd := m.viewport.Update(msg)
			m.viewport = u
			if m.viewport.AtBottom() {
				m.newOutput = false
			}
			cmds = append(cmds, cmd)
		}

	case sessionLoadedMsg:
		if msg.sessionID != m.session.ID {
			return m, nil
		}
		m.rendering = false
		if msg.err != nil {
			m.messages = nil
			m.currentMsgID = ""
			m.renderView()
			return m, util.ReportError(msg.err)
		}
		m.messages = msg.messages
		m.currentMsgID = ""
		if len(m.messages) > 0 {
			m.currentMsgID = m.messages[len(m.messages)-1].ID
		}
		m.cachedContent = make(map[string]cacheItem)
		m.renderView()
		m.viewport.GotoBottom()
		m.newOutput = false
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == m.session.ID {
			m.session = msg.Payload
			if m.session.SummaryMessageID == m.currentMsgID {
				delete(m.cachedContent, m.currentMsgID)
				m.renderView()
			}
		}
	case pubsub.Event[message.Message]:
		needsRerender := false
		if msg.Type == pubsub.CreatedEvent {
			if msg.Payload.SessionID == m.session.ID {

				messageExists := false
				for _, v := range m.messages {
					if v.ID == msg.Payload.ID {
						messageExists = true
						break
					}
				}

				if !messageExists {
					if len(m.messages) > 0 {
						lastMsgID := m.messages[len(m.messages)-1].ID
						delete(m.cachedContent, lastMsgID)
					}

					m.messages = append(m.messages, msg.Payload)
					delete(m.cachedContent, m.currentMsgID)
					m.currentMsgID = msg.Payload.ID
					needsRerender = true
				}
			}
			// There are tool calls from the child task
			for _, v := range m.messages {
				for _, c := range v.ToolCalls() {
					if c.ID == msg.Payload.SessionID {
						delete(m.cachedContent, v.ID)
						needsRerender = true
					}
				}
			}
		} else if msg.Type == pubsub.UpdatedEvent && msg.Payload.SessionID == m.session.ID {
			for i, v := range m.messages {
				if v.ID == msg.Payload.ID {
					m.messages[i] = msg.Payload
					delete(m.cachedContent, msg.Payload.ID)
					needsRerender = true
					break
				}
			}
		}
		if needsRerender {
			wasAtBottom := m.viewport.AtBottom()
			m.renderView()
			if len(m.messages) > 0 {
				if (msg.Type == pubsub.CreatedEvent) ||
					(msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == m.messages[len(m.messages)-1].ID) {
					if wasAtBottom {
						m.viewport.GotoBottom()
						m.newOutput = false
					} else {
						m.newOutput = true
					}
				}
			}
		}
	}

	spinner, cmd := m.spinner.Update(msg)
	m.spinner = spinner
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *messagesCmp) IsAgentWorking() bool {
	if m.app == nil || m.app.CoderAgent == nil {
		return false
	}
	return m.app.CoderAgent.IsSessionBusy(m.session.ID)
}

func formatTimeDifference(unixTime1, unixTime2 int64) string {
	diffSeconds := float64(math.Abs(float64(unixTime2 - unixTime1)))

	if diffSeconds < 60 {
		return fmt.Sprintf("%.1fs", diffSeconds)
	}

	minutes := int(diffSeconds / 60)
	seconds := int(diffSeconds) % 60
	return fmt.Sprintf("%dm%ds", minutes, seconds)
}

func (m *messagesCmp) renderView() {
	m.uiMessages = make([]uiMessage, 0)
	pos := 0
	baseStyle := styles.BaseStyle()

	if m.width == 0 {
		return
	}
	contentWidth := layout.ContentWidth(m.width)
	for inx, msg := range m.messages {
		switch msg.Role {
		case message.User:
			if cache, ok := m.cachedContent[msg.ID]; ok && cache.width == m.width {
				m.uiMessages = append(m.uiMessages, cache.content...)
				for _, cached := range cache.content {
					pos += cached.height + 1
				}
				continue
			}
			userMsg := renderUserMessage(
				msg,
				msg.ID == m.currentMsgID,
				contentWidth,
				pos,
			)
			m.uiMessages = append(m.uiMessages, userMsg)
			m.cachedContent[msg.ID] = cacheItem{
				width:   m.width,
				content: []uiMessage{userMsg},
			}
			pos += userMsg.height + 1 // + 1 for spacing
		case message.Assistant:
			if cache, ok := m.cachedContent[msg.ID]; ok && cache.width == m.width {
				m.uiMessages = append(m.uiMessages, cache.content...)
				for _, cached := range cache.content {
					pos += cached.height + 1
				}
				continue
			}
			isSummary := m.session.SummaryMessageID == msg.ID

			assistantMessages := renderAssistantMessage(
				msg,
				inx,
				m.messages,
				m.app.Messages,
				m.currentMsgID,
				isSummary,
				contentWidth,
				pos,
			)
			for _, msg := range assistantMessages {
				m.uiMessages = append(m.uiMessages, msg)
				pos += msg.height + 1 // + 1 for spacing
			}
			m.cachedContent[msg.ID] = cacheItem{
				width:   m.width,
				content: assistantMessages,
			}
		}
	}

	messages := make([]string, 0)
	for _, v := range m.uiMessages {
		messages = append(messages, lipgloss.JoinVertical(lipgloss.Left, v.content),
			baseStyle.
				Width(contentWidth).
				Render(
					"",
				),
		)
	}

	m.viewport.SetContent(
		baseStyle.
			Width(m.width).
			Align(lipgloss.Center).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Top,
					messages...,
				),
			),
	)
}

func (m *messagesCmp) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	activity := m.activity()
	bodyHeight := m.height
	if activity != "" {
		bodyHeight--
	}
	bodyHeight = max(0, bodyHeight)

	body := ""
	switch {
	case m.rendering:
		// Loading is communicated by the compact activity row. Keeping the body
		// empty avoids showing stale session content while a new session loads.
	case len(m.messages) == 0:
		body = m.initialScreen(bodyHeight)
	default:
		view := m.viewport
		view.Width = m.width
		view.Height = bodyHeight
		if m.viewport.AtBottom() {
			view.GotoBottom()
		}
		body = view.View()
	}

	return m.renderShell(body, bodyHeight, activity)
}

// renderShell gives the transcript a strict row budget. Individual renderers
// may return more content than expected after wrapping or terminal-width
// differences; clipping here prevents the transcript from displacing the
// composer or the application status line on small terminals.
func (m *messagesCmp) renderShell(body string, bodyHeight int, activity string) string {
	baseStyle := styles.BaseStyle()
	parts := make([]string, 0, 2)
	if bodyHeight > 0 {
		parts = append(parts, baseStyle.
			Width(m.width).
			Height(bodyHeight).
			Render(truncateViewHeight(body, bodyHeight)))
	}
	if activity != "" {
		parts = append(parts, activity)
	}
	return truncateViewHeight(
		baseStyle.Width(m.width).Render(lipgloss.JoinVertical(lipgloss.Top, parts...)),
		m.height,
	)
}

func truncateViewHeight(content string, height int) string {
	if height <= 0 || content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func hasToolsWithoutResponse(messages []message.Message) bool {
	toolCalls := make([]message.ToolCall, 0)
	toolResults := make([]message.ToolResult, 0)
	for _, m := range messages {
		toolCalls = append(toolCalls, m.ToolCalls()...)
		toolResults = append(toolResults, m.ToolResults()...)
	}

	for _, v := range toolCalls {
		found := false
		for _, r := range toolResults {
			if v.ID == r.ToolCallID {
				found = true
				break
			}
		}
		if !found && v.Finished {
			return true
		}
	}
	return false
}

func hasUnfinishedToolCalls(messages []message.Message) bool {
	toolCalls := make([]message.ToolCall, 0)
	for _, m := range messages {
		toolCalls = append(toolCalls, m.ToolCalls()...)
	}
	for _, v := range toolCalls {
		if !v.Finished {
			return true
		}
	}
	return false
}

// activity renders the only contextual row owned by the transcript. General
// send/actions/help shortcuts live in the root status bar, so they are not
// repeated here. New output takes priority because it requires user action.
func (m *messagesCmp) activity() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	ellipsis := styles.Glyph("…", "...")
	down := styles.Glyph("↓", "v")
	text := ""
	if m.newOutput {
		text = lipgloss.JoinHorizontal(
			lipgloss.Left,
			baseStyle.Foreground(t.Primary()).Bold(true).Render("new output "+down),
			baseStyle.Foreground(t.TextMuted()).Render("  ·  pgdn follow"),
		)
	} else if m.rendering {
		text = baseStyle.Foreground(t.Primary()).Bold(true).
			Render(fmt.Sprintf("%s Loading conversation%s", m.spinner.View(), ellipsis))
	} else if m.IsAgentWorking() && len(m.messages) > 0 {
		task := "Thinking" + ellipsis
		lastMessage := m.messages[len(m.messages)-1]
		if hasToolsWithoutResponse(m.messages) {
			task = "Waiting for tool response" + ellipsis
		} else if hasUnfinishedToolCalls(m.messages) {
			task = "Building tool call" + ellipsis
		} else if !lastMessage.IsFinished() {
			task = "Generating" + ellipsis
		}
		text = baseStyle.Foreground(t.Primary()).Bold(true).
			Render(fmt.Sprintf("%s %s", m.spinner.View(), task))
	}
	if text == "" {
		return ""
	}
	text = ansi.Truncate(text, max(1, m.width), ellipsis)
	return baseStyle.
		Width(m.width).
		Render(text)
}

func (m *messagesCmp) initialScreen(height int) string {
	if m.width <= 0 || height <= 0 {
		return ""
	}
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	ellipsis := styles.Glyph("…", "...")
	line := func(value string) string {
		return ansi.Truncate(value, max(1, m.width), ellipsis)
	}
	labeledExample := func(label, example string) string {
		return line(
			baseStyle.Foreground(t.Primary()).Bold(true).Render(label+": ") +
				baseStyle.Foreground(t.TextMuted()).Render(example),
		)
	}

	cta := "Type a request in the composer below."
	if m.width < 38 {
		cta = "Type a request below."
	}
	if height == 1 {
		return baseStyle.Foreground(t.Text()).Render(line(cta))
	}

	lines := []string{
		baseStyle.Foreground(t.TextEmphasized()).Bold(true).Render(line("Start with a request")),
		baseStyle.Foreground(t.TextMuted()).Render(line(cta)),
	}
	if height >= 4 {
		if height >= 5 {
			lines = append(lines, "")
		}
		lines = append(lines, baseStyle.Foreground(t.TextMuted()).Bold(true).Render("Examples"))
	}
	examples := []string{
		labeledExample("Inspect", "map the repository architecture"),
		labeledExample("Change", "add tests for a focused behavior"),
		labeledExample("Verify", "run checks and explain failures"),
	}
	for _, example := range examples {
		if len(lines) >= height {
			break
		}
		lines = append(lines, example)
	}
	return truncateViewHeight(
		baseStyle.Width(m.width).Render(lipgloss.JoinVertical(lipgloss.Left, lines...)),
		height,
	)
}

func (m *messagesCmp) rerender() {
	for _, msg := range m.messages {
		delete(m.cachedContent, msg.ID)
	}
	m.renderView()
}

func (m *messagesCmp) SetSize(width, height int) tea.Cmd {
	if m.width == width && m.height == height {
		return nil
	}
	wasAtBottom := m.viewport.AtBottom()
	m.width = max(0, width)
	m.height = max(0, height)
	m.viewport.Width = max(0, width)
	// Keep one row available for loading, live activity, or a new-output cue.
	// View expands the transcript into that row when no activity is present.
	m.viewport.Height = max(0, height-1)
	m.attachments.Width = max(0, width+40)
	m.attachments.Height = 3
	m.rerender()
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	return nil
}

func (m *messagesCmp) GetSize() (int, int) {
	return m.width, m.height
}

func (m *messagesCmp) SetSession(session session.Session) tea.Cmd {
	if m.session.ID == session.ID {
		return nil
	}
	m.session = session
	m.rendering = true
	m.newOutput = false
	sessionID := session.ID
	return func() tea.Msg {
		messages, err := m.app.Messages.List(context.Background(), sessionID)
		return sessionLoadedMsg{sessionID: sessionID, messages: messages, err: err}
	}
}

func (m *messagesCmp) BindingKeys() []key.Binding {
	return []key.Binding{
		m.viewport.KeyMap.PageDown,
		m.viewport.KeyMap.PageUp,
		m.viewport.KeyMap.HalfPageUp,
		m.viewport.KeyMap.HalfPageDown,
	}
}

func NewMessagesCmp(app *app.App) tea.Model {
	s := spinner.New()
	s.Spinner = spinner.Pulse
	vp := viewport.New(0, 0)
	attachmets := viewport.New(0, 0)
	vp.KeyMap.PageUp = messageKeys.PageUp
	vp.KeyMap.PageDown = messageKeys.PageDown
	vp.KeyMap.HalfPageUp = messageKeys.HalfPageUp
	vp.KeyMap.HalfPageDown = messageKeys.HalfPageDown
	return &messagesCmp{
		app:           app,
		cachedContent: make(map[string]cacheItem),
		viewport:      vp,
		spinner:       s,
		attachments:   attachmets,
	}
}
