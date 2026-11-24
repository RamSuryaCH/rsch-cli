package logs

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type DetailComponent interface {
	tea.Model
	layout.Sizeable
	layout.Bindings
	SetFocused(bool)
	IsFocused() bool
}

type detailCmp struct {
	width, height int
	currentLog    logging.LogMessage
	viewport      viewport.Model
	focused       bool
}

type detailKeyMap struct {
	LineUp     key.Binding
	LineDown   key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
	GotoTop    key.Binding
	GotoBottom key.Binding
}

var detailKeys = detailKeyMap{
	LineUp: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "scroll details up"),
	),
	LineDown: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "scroll details down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page details up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page details down"),
	),
	GotoTop: key.NewBinding(
		key.WithKeys("home"),
		key.WithHelp("home", "first detail line"),
	),
	GotoBottom: key.NewBinding(
		key.WithKeys("end"),
		key.WithHelp("end", "last detail line"),
	),
}

func (i *detailCmp) Init() tea.Cmd {
	// The table owns selection and publishes it after initialization.
	i.updateContent()
	return nil
}

func (i *detailCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case selectedLogMsg:
		changed := msg.ID != i.currentLog.ID
		i.currentLog = logging.LogMessage(msg)
		i.updateContent()
		if changed {
			i.viewport.GotoTop()
		}
	case tea.KeyMsg:
		if !i.focused {
			return i, nil
		}
		switch {
		case key.Matches(msg, detailKeys.GotoTop):
			i.viewport.GotoTop()
			return i, nil
		case key.Matches(msg, detailKeys.GotoBottom):
			i.viewport.GotoBottom()
			return i, nil
		}
		updated, cmd := i.viewport.Update(msg)
		i.viewport = updated
		return i, cmd
	}

	return i, nil
}

func (i *detailCmp) updateContent() {
	if i.currentLog.ID == "" {
		i.viewport.SetContent("")
		return
	}
	var content strings.Builder
	t := theme.CurrentTheme()
	currentLog := sanitizeLog(i.currentLog)

	// Format the header with timestamp and level
	timeStyle := lipgloss.NewStyle().Foreground(t.TextMuted())
	levelStyle := getLevelStyle(currentLog.Level)

	header := lipgloss.JoinHorizontal(
		lipgloss.Center,
		timeStyle.Render(currentLog.Time.Format(time.RFC3339)),
		"  ",
		levelStyle.Render(currentLog.Level),
	)

	content.WriteString(lipgloss.NewStyle().Bold(true).Render(header))
	content.WriteString("\n\n")

	// Message with styling
	messageStyle := lipgloss.NewStyle().Bold(true).Foreground(t.Text())
	content.WriteString(messageStyle.Render("Message:"))
	content.WriteString("\n")
	content.WriteString(lipgloss.NewStyle().Padding(0, 2).Render(currentLog.Message))
	content.WriteString("\n\n")

	// Attributes section
	if len(currentLog.Attributes) > 0 {
		attrHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(t.Text())
		content.WriteString(attrHeaderStyle.Render("Attributes:"))
		content.WriteString("\n")

		// Create a table-like display for attributes
		keyStyle := lipgloss.NewStyle().Foreground(t.Primary()).Bold(true)
		valueStyle := lipgloss.NewStyle().Foreground(t.Text())

		for _, attr := range currentLog.Attributes {
			attrLine := fmt.Sprintf("%s: %s",
				keyStyle.Render(attr.Key),
				valueStyle.Render(attr.Value),
			)
			content.WriteString(lipgloss.NewStyle().Padding(0, 2).Render(attrLine))
			content.WriteString("\n")
		}
	}

	// The viewport only scrolls vertically, so wrap long lines to make every
	// part of messages and attributes reachable without horizontal scrolling.
	i.viewport.SetContent(ansi.Hardwrap(content.String(), max(1, i.viewport.Width), true))
}

func getLevelStyle(level string) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	t := theme.CurrentTheme()

	switch strings.ToLower(level) {
	case "info":
		return style.Foreground(t.Info())
	case "warn", "warning":
		return style.Foreground(t.Warning())
	case "error", "err":
		return style.Foreground(t.Error())
	case "debug":
		return style.Foreground(t.Success())
	default:
		return style.Foreground(t.Text())
	}
}

func (i *detailCmp) View() string {
	if i.width <= 0 || i.height <= 0 {
		return ""
	}
	t := theme.CurrentTheme()
	labelStyle := styles.BaseStyle().Foreground(t.TextMuted()).Bold(i.focused)
	if i.focused {
		labelStyle = labelStyle.Foreground(t.Primary())
	}
	label := labelStyle.Render(ansi.Truncate(styles.SelectionLabel("Log details", i.focused), i.width, "…"))
	if i.height <= 1 {
		return label
	}
	content := styles.ForceReplaceBackgroundWithLipgloss(i.viewport.View(), t.Background())
	return lipgloss.JoinVertical(lipgloss.Left, label, content)
}

func (i *detailCmp) GetSize() (int, int) {
	return i.width, i.height
}

func (i *detailCmp) SetSize(width int, height int) tea.Cmd {
	i.width = width
	i.height = height
	i.viewport.Width = max(0, i.width)
	// Reserve one line for the persistent, non-color focus indicator.
	i.viewport.Height = max(0, i.height-1)
	i.updateContent()
	return nil
}

func (i *detailCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(detailKeys)
}

func (i *detailCmp) SetFocused(focused bool) {
	i.focused = focused
}

func (i *detailCmp) IsFocused() bool {
	return i.focused
}

func NewLogsDetails() DetailComponent {
	view := viewport.New(0, 0)
	view.KeyMap = viewport.KeyMap{
		PageDown: detailKeys.PageDown,
		PageUp:   detailKeys.PageUp,
		Down:     detailKeys.LineDown,
		Up:       detailKeys.LineUp,
	}
	return &detailCmp{viewport: view}
}
