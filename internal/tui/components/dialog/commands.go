package dialog

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	utilComponents "github.com/opencode-ai/opencode/internal/tui/components/util"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// Command represents a command that can be executed
type Command struct {
	ID          string
	Title       string
	Description string
	Handler     func(cmd Command) tea.Cmd
}

func (ci Command) Render(selected bool, width int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	width = max(1, width)
	marker := "  "
	if selected {
		marker = "› "
	}

	// Command metadata can come from workspace-defined custom commands. Keep a
	// palette row on one line and prevent labels from emitting terminal control
	// sequences before applying trusted TUI styling.
	id := commandLineText(ci.ID)
	title := commandLineText(ci.Title)
	description := commandLineText(ci.Description)
	primary := title
	if id != "" {
		primary = "/" + strings.TrimPrefix(id, "/")
		if title != "" {
			primary += "  " + title
		}
	}
	if primary == "" {
		primary = "Untitled command"
	}

	available := max(1, width-3)
	primary = ansi.Truncate(primary, available, "…")
	line := primary
	if description != "" {
		remaining := available - lipgloss.Width(primary) - 2
		if remaining > 1 {
			detail := ansi.Truncate(description, remaining, "…")
			if selected {
				line += "  " + detail
			} else {
				line += baseStyle.Foreground(t.TextMuted()).Render("  " + detail)
			}
		}
	}
	style := baseStyle.Width(width).Padding(0, 1).Foreground(t.Text())
	if selected {
		style = style.Background(t.Primary()).Foreground(t.Background()).Bold(true)
	}
	return style.Render(marker + line)
}

func commandLineText(value string) string {
	return strings.Join(strings.Fields(styles.SanitizeTerminalText(value)), " ")
}

// CommandSelectedMsg is sent when a command is selected
type CommandSelectedMsg struct {
	Command Command
}

// CloseCommandDialogMsg is sent when the command dialog is closed
type CloseCommandDialogMsg struct{}

// ShowCommandDialogMsg requests the application-level palette from an input
// component. Keeping this as a message avoids coupling the editor to the root
// model while allowing slash-triggered discovery.
type ShowCommandDialogMsg struct{}

// CommandDialog interface for the command selection dialog
type CommandDialog interface {
	tea.Model
	layout.Bindings
	SetCommands(commands []Command)
}

type commandDialogCmp struct {
	listView utilComponents.SimpleList[Command]
	search   textinput.Model
	commands []Command
	width    int
	height   int
}

type commandKeyMap struct {
	Enter  key.Binding
	Escape key.Binding
}

var commandKeys = commandKeyMap{
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select command"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	),
}

func (c *commandDialogCmp) Init() tea.Cmd {
	// Init is called every time the palette opens. A prior query should never
	// make a fresh palette look empty or hide commands without explanation.
	c.search.Reset()
	c.search.Focus()
	c.filter()
	return tea.Batch(c.listView.Init(), textinput.Blink)
}

func (c *commandDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, commandKeys.Enter):
			selectedItem, idx := c.listView.GetSelectedItem()
			if idx != -1 {
				return c, util.CmdHandler(CommandSelectedMsg{
					Command: selectedItem,
				})
			}
		case key.Matches(msg, commandKeys.Escape):
			return c, util.CmdHandler(CloseCommandDialogMsg{})
		}
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height
	}
	var searchCmd tea.Cmd
	previousQuery := c.search.Value()
	c.search, searchCmd = c.search.Update(msg)
	if c.search.Value() != previousQuery {
		c.filter()
	}

	u, cmd := c.listView.Update(msg)
	c.listView = u.(utilComponents.SimpleList[Command])
	cmds = append(cmds, cmd, searchCmd)

	return c, tea.Batch(cmds...)
}

func (c *commandDialogCmp) View() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	terminalWidth := c.width
	if terminalWidth <= 0 {
		terminalWidth = 80
	}
	maxWidth := max(8, min(72, terminalWidth-6))
	c.listView.SetMaxWidth(maxWidth - 2)
	verticalPadding := 1
	chromeRows := 7 // title, search, hint, border, and vertical padding
	if c.height > 0 && c.height < 12 {
		verticalPadding = 0
		chromeRows = 5
	}
	visibleItems := 8
	if c.height > 0 {
		visibleItems = max(1, min(8, c.height-chromeRows))
	}
	c.listView.SetMaxVisibleItems(visibleItems)
	c.search.Width = max(1, maxWidth-4)
	search := baseStyle.Foreground(t.Text()).Width(maxWidth - 2).Render("/" + c.search.View())
	filtered := c.listView.GetItems()

	countLabel := fmt.Sprintf("%d commands", len(filtered))
	if len(filtered) == 1 {
		countLabel = "1 command"
	}
	if strings.TrimSpace(c.search.Value()) != "" {
		countLabel = fmt.Sprintf("%d matches", len(filtered))
		if len(filtered) == 1 {
			countLabel = "1 match"
		}
	}
	title := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Width(maxWidth).
		Padding(0, 1).
		Render(ansi.Truncate("Commands  ·  "+countLabel, max(1, maxWidth-2), "…"))

	list := c.listView.View()
	if len(filtered) == 0 {
		list = c.emptyState(maxWidth - 2)
	}

	hint := "↑↓ navigate · enter run · esc close"
	if len(filtered) == 0 {
		if strings.TrimSpace(c.search.Value()) == "" {
			hint = "esc close"
		} else {
			hint = "backspace edit · esc close"
		}
	} else if len(filtered) > visibleItems {
		_, selected := c.listView.GetSelectedItem()
		hint = fmt.Sprintf("↑↓ navigate · %d/%d · enter run · esc close", selected+1, len(filtered))
	}

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		search,
		baseStyle.Width(maxWidth).Render(list),
		baseStyle.Foreground(t.TextMuted()).Width(maxWidth).Render(
			ansi.Truncate(hint, maxWidth, "…"),
		),
	)

	return baseStyle.Padding(verticalPadding, 1).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(maxWidth).
		Render(content)
}

func (c *commandDialogCmp) emptyState(width int) string {
	query := commandLineText(c.search.Value())
	message := "No commands available"
	if query != "" && len(c.commands) > 0 {
		message = fmt.Sprintf("No matches for %q", query)
	}
	return styles.BaseStyle().
		Foreground(theme.CurrentTheme().TextMuted()).
		Padding(0, 1).
		Width(max(1, width)).
		Render(ansi.Truncate(message, max(1, width-2), "…"))
}

func (c *commandDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(commandKeys)
}

func (c *commandDialogCmp) SetCommands(commands []Command) {
	c.commands = append([]Command(nil), commands...)
	c.filter()
}

func (c *commandDialogCmp) filter() {
	query := strings.TrimSpace(strings.ToLower(c.search.Value()))
	filtered := make([]Command, 0, len(c.commands))
	for _, command := range c.commands {
		searchable := commandLineText(command.ID + " " + command.Title + " " + command.Description)
		if query == "" || fuzzyCommandMatch(query, strings.ToLower(searchable)) {
			filtered = append(filtered, command)
		}
	}
	c.listView.SetItems(filtered)
}

// fuzzyCommandMatch uses subsequence matching, which is forgiving enough for
// quick terminal use while keeping filtering deterministic and dependency-free.
func fuzzyCommandMatch(query, value string) bool {
	if query == "" {
		return true
	}
	qi := 0
	for _, r := range value {
		if qi < len([]rune(query)) && r == []rune(query)[qi] {
			qi++
		}
	}
	return qi == len([]rune(query))
}

// NewCommandDialogCmp creates a new command selection dialog
func NewCommandDialogCmp() CommandDialog {
	listView := utilComponents.NewSimpleList[Command](
		[]Command{},
		8,
		"No commands available",
		false,
	)
	search := textinput.New()
	search.Placeholder = "filter commands"
	search.Prompt = ""
	search.CharLimit = 200
	return &commandDialogCmp{
		listView: listView,
		search:   search,
	}
}
