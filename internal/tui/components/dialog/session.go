package dialog

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// SessionSelectedMsg is sent when a session is selected
type SessionSelectedMsg struct {
	Session session.Session
}

// CloseSessionDialogMsg is sent when the session dialog is closed
type CloseSessionDialogMsg struct{}

// SessionDialog interface for the session switching dialog
type SessionDialog interface {
	tea.Model
	layout.Bindings
	SetSessions(sessions []session.Session)
	SetSelectedSession(sessionID string)
}

type sessionDialogCmp struct {
	sessions          []session.Session
	selectedIdx       int
	width             int
	height            int
	selectedSessionID string
}

type sessionKeyMap struct {
	Up     key.Binding
	Down   key.Binding
	Enter  key.Binding
	Escape key.Binding
	J      key.Binding
	K      key.Binding
}

var sessionKeys = sessionKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "previous session"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "next session"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select session"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	),
	J: key.NewBinding(
		key.WithKeys("j"),
		key.WithHelp("j", "next session"),
	),
	K: key.NewBinding(
		key.WithKeys("k"),
		key.WithHelp("k", "previous session"),
	),
}

func (s *sessionDialogCmp) Init() tea.Cmd {
	return nil
}

func (s *sessionDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, sessionKeys.Up) || key.Matches(msg, sessionKeys.K):
			if s.selectedIdx > 0 {
				s.selectedIdx--
			}
			return s, nil
		case key.Matches(msg, sessionKeys.Down) || key.Matches(msg, sessionKeys.J):
			if s.selectedIdx < len(s.sessions)-1 {
				s.selectedIdx++
			}
			return s, nil
		case key.Matches(msg, sessionKeys.Enter):
			if len(s.sessions) > 0 {
				return s, util.CmdHandler(SessionSelectedMsg{
					Session: s.sessions[s.selectedIdx],
				})
			}
		case key.Matches(msg, sessionKeys.Escape):
			return s, util.CmdHandler(CloseSessionDialogMsg{})
		}
	case tea.WindowSizeMsg:
		s.width = msg.Width
		s.height = msg.Height
	}
	return s, nil
}

func (s *sessionDialogCmp) View() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	terminalWidth, terminalHeight := s.width, s.height
	if terminalWidth <= 0 {
		terminalWidth = 80
	}
	if terminalHeight <= 0 {
		terminalHeight = 24
	}

	// Borders and padding are useful chrome when space permits, but neither is
	// allowed to make the dialog larger than a very small terminal.
	borderWidth, borderHeight := 0, 0
	outerStyle := baseStyle
	if terminalWidth >= 3 && terminalHeight >= 3 {
		borderWidth, borderHeight = 2, 2
		outerStyle = outerStyle.
			Border(lipgloss.RoundedBorder()).
			BorderBackground(t.Background()).
			BorderForeground(t.TextMuted())
	}
	horizontalPadding := 0
	if terminalWidth-borderWidth >= 8 {
		horizontalPadding = 1
		outerStyle = outerStyle.Padding(0, horizontalPadding)
	}
	maxContentWidth := max(1, terminalWidth-borderWidth-horizontalPadding*2)
	maxContentHeight := max(1, terminalHeight-borderHeight)

	if len(s.sessions) == 0 {
		message := "No sessions available"
		contentWidth := min(maxContentWidth, lipgloss.Width(message))
		return outerStyle.Width(contentWidth + horizontalPadding*2).Render(ansi.Truncate(message, contentWidth, "…"))
	}

	const markerWidth = 4 // navigation cursor, current marker, and spacing
	titles := make([]string, len(s.sessions))
	desiredContentWidth := 30
	for i, sess := range s.sessions {
		titles[i] = sessionDisplayTitle(sess.Title)
		desiredContentWidth = max(desiredContentWidth, markerWidth+lipgloss.Width(titles[i]))
	}
	contentWidth := min(desiredContentWidth, maxContentWidth)

	// Reserve a title line where possible, then devote the remaining height to
	// rows. On a one-line terminal the selected row is more useful than chrome.
	headerRows := 0
	if maxContentHeight >= 2 {
		headerRows = 1
	}
	separatorRows := 0
	if maxContentHeight >= 4 {
		separatorRows = 1
	}
	maxVisibleSessions := min(len(s.sessions), max(1, maxContentHeight-headerRows-separatorRows))

	// Build the session list
	sessionItems := make([]string, 0, maxVisibleSessions)
	startIdx := 0

	// If we have more sessions than can be displayed, adjust the start index
	if len(s.sessions) > maxVisibleSessions {
		// Center the selected item when possible
		halfVisible := maxVisibleSessions / 2
		if s.selectedIdx >= halfVisible && s.selectedIdx < len(s.sessions)-halfVisible {
			startIdx = s.selectedIdx - halfVisible
		} else if s.selectedIdx >= len(s.sessions)-halfVisible {
			startIdx = len(s.sessions) - maxVisibleSessions
		}
	}

	endIdx := min(startIdx+maxVisibleSessions, len(s.sessions))

	for i := startIdx; i < endIdx; i++ {
		sess := s.sessions[i]
		itemStyle := baseStyle.Width(contentWidth)

		if i == s.selectedIdx {
			itemStyle = itemStyle.
				Background(t.Primary()).
				Foreground(t.Background()).
				Bold(true)
		}

		cursor := " "
		if i == s.selectedIdx {
			cursor = "›"
		}
		current := " "
		if sess.ID != "" && sess.ID == s.selectedSessionID {
			current = "●"
		}
		prefix := cursor + " " + current + " "
		row := ansi.Truncate(prefix+titles[i], contentWidth, "…")
		sessionItems = append(sessionItems, itemStyle.Render(row))
	}

	title := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Width(contentWidth).
		Render(ansi.Truncate("Switch Session", contentWidth, "…"))

	contentRows := make([]string, 0, headerRows+separatorRows+len(sessionItems))
	if headerRows > 0 {
		contentRows = append(contentRows, title)
	}
	if separatorRows > 0 {
		contentRows = append(contentRows, baseStyle.Width(contentWidth).Render(""))
	}
	contentRows = append(contentRows, sessionItems...)
	content := lipgloss.JoinVertical(lipgloss.Left, contentRows...)

	return outerStyle.Width(contentWidth + horizontalPadding*2).Render(content)
}

func sessionDisplayTitle(value string) string {
	value = styles.SanitizeTerminalText(value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "Untitled session"
	}
	return value
}

func (s *sessionDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(sessionKeys)
}

func (s *sessionDialogCmp) SetSessions(sessions []session.Session) {
	s.sessions = sessions

	// If we have a selected session ID, find its index
	if s.selectedSessionID != "" {
		for i, sess := range sessions {
			if sess.ID == s.selectedSessionID {
				s.selectedIdx = i
				return
			}
		}
	}

	// Default to first session if selected not found
	s.selectedIdx = 0
}

func (s *sessionDialogCmp) SetSelectedSession(sessionID string) {
	s.selectedSessionID = sessionID

	// Update the selected index if sessions are already loaded
	if len(s.sessions) > 0 {
		for i, sess := range s.sessions {
			if sess.ID == sessionID {
				s.selectedIdx = i
				return
			}
		}
	}
}

// NewSessionDialogCmp creates a new session switching dialog
func NewSessionDialogCmp() SessionDialog {
	return &sessionDialogCmp{
		sessions:          []session.Session{},
		selectedIdx:       0,
		selectedSessionID: "",
	}
}
