package dialog

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type argumentsDialogKeyMap struct {
	Enter  key.Binding
	Escape key.Binding
}

// ShortHelp implements key.Map.
func (k argumentsDialogKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "confirm"),
		),
		key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

// FullHelp implements key.Map.
func (k argumentsDialogKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// ShowMultiArgumentsDialogMsg is a message that is sent to show the multi-arguments dialog.
type ShowMultiArgumentsDialogMsg struct {
	CommandID string
	Content   string
	ArgNames  []string
}

// CloseMultiArgumentsDialogMsg is a message that is sent when the multi-arguments dialog is closed.
type CloseMultiArgumentsDialogMsg struct {
	Submit    bool
	CommandID string
	Content   string
	Args      map[string]string
}

// MultiArgumentsDialogCmp is a component that asks the user for multiple command arguments.
type MultiArgumentsDialogCmp struct {
	width, height int
	inputs        []textinput.Model
	focusIndex    int
	keys          argumentsDialogKeyMap
	commandID     string
	content       string
	argNames      []string
}

// NewMultiArgumentsDialogCmp creates a new MultiArgumentsDialogCmp.
func NewMultiArgumentsDialogCmp(commandID, content string, argNames []string) MultiArgumentsDialogCmp {
	t := theme.CurrentTheme()
	inputs := make([]textinput.Model, len(argNames))

	for i, name := range argNames {
		ti := textinput.New()
		ti.Placeholder = fmt.Sprintf("Enter value for %s...", name)
		ti.Width = 40
		ti.Prompt = ""
		ti.PlaceholderStyle = ti.PlaceholderStyle.Background(t.Background())
		ti.PromptStyle = ti.PromptStyle.Background(t.Background())
		ti.TextStyle = ti.TextStyle.Background(t.Background())

		// Only focus the first input initially
		if i == 0 {
			ti.Focus()
			ti.PromptStyle = ti.PromptStyle.Foreground(t.Primary())
			ti.TextStyle = ti.TextStyle.Foreground(t.Primary())
		} else {
			ti.Blur()
		}

		inputs[i] = ti
	}

	return MultiArgumentsDialogCmp{
		inputs:     inputs,
		keys:       argumentsDialogKeyMap{},
		commandID:  commandID,
		content:    content,
		argNames:   argNames,
		focusIndex: 0,
	}
}

// Init implements tea.Model.
func (m MultiArgumentsDialogCmp) Init() tea.Cmd {
	// Make sure only the first input is focused
	for i := range m.inputs {
		if i == 0 {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}

	return textinput.Blink
}

// Update implements tea.Model.
func (m MultiArgumentsDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(m.inputs) == 0 {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.SetSize(msg.Width, msg.Height)
		case tea.KeyMsg:
			if msg.Type == tea.KeyEnter || msg.Type == tea.KeyEsc {
				return m, util.CmdHandler(CloseMultiArgumentsDialogMsg{
					Submit: msg.Type == tea.KeyEnter, CommandID: m.commandID, Content: m.content,
				})
			}
		}
		return m, nil
	}
	var cmds []tea.Cmd
	t := theme.CurrentTheme()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			return m, util.CmdHandler(CloseMultiArgumentsDialogMsg{
				Submit:    false,
				CommandID: m.commandID,
				Content:   m.content,
				Args:      nil,
			})
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			// If we're on the last input, submit the form
			if m.focusIndex == len(m.inputs)-1 {
				args := make(map[string]string)
				for i, name := range m.argNames {
					args[name] = m.inputs[i].Value()
				}
				return m, util.CmdHandler(CloseMultiArgumentsDialogMsg{
					Submit:    true,
					CommandID: m.commandID,
					Content:   m.content,
					Args:      args,
				})
			}
			// Otherwise, move to the next input
			m.inputs[m.focusIndex].Blur()
			m.focusIndex++
			m.inputs[m.focusIndex].Focus()
			m.inputs[m.focusIndex].PromptStyle = m.inputs[m.focusIndex].PromptStyle.Foreground(t.Primary())
			m.inputs[m.focusIndex].TextStyle = m.inputs[m.focusIndex].TextStyle.Foreground(t.Primary())
		case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
			// Move to the next input
			m.inputs[m.focusIndex].Blur()
			m.focusIndex = (m.focusIndex + 1) % len(m.inputs)
			m.inputs[m.focusIndex].Focus()
			m.inputs[m.focusIndex].PromptStyle = m.inputs[m.focusIndex].PromptStyle.Foreground(t.Primary())
			m.inputs[m.focusIndex].TextStyle = m.inputs[m.focusIndex].TextStyle.Foreground(t.Primary())
		case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
			// Move to the previous input
			m.inputs[m.focusIndex].Blur()
			m.focusIndex = (m.focusIndex - 1 + len(m.inputs)) % len(m.inputs)
			m.inputs[m.focusIndex].Focus()
			m.inputs[m.focusIndex].PromptStyle = m.inputs[m.focusIndex].PromptStyle.Foreground(t.Primary())
			m.inputs[m.focusIndex].TextStyle = m.inputs[m.focusIndex].TextStyle.Foreground(t.Primary())
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	// Update the focused input
	var cmd tea.Cmd
	m.inputs[m.focusIndex], cmd = m.inputs[m.focusIndex].Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View implements tea.Model.
func (m MultiArgumentsDialogCmp) View() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}

	// Allocate chrome before laying out fields, including on tiny terminals.
	outerStyle := baseStyle
	borderWidth, borderHeight := 0, 0
	if width >= 4 && height >= 4 {
		borderWidth, borderHeight = 2, 2
		outerStyle = outerStyle.Border(lipgloss.RoundedBorder()).
			BorderBackground(t.Background()).BorderForeground(t.TextMuted())
	}
	horizontalPadding, verticalPadding := 0, 0
	if width >= 12 {
		horizontalPadding = 2
	}
	if height >= 10 {
		verticalPadding = 1
	}
	outerStyle = outerStyle.Padding(verticalPadding, horizontalPadding)
	contentWidth := min(60, max(1, width-borderWidth-2*horizontalPadding))
	contentHeight := max(1, height-borderHeight-2*verticalPadding)
	fieldPadding := 0
	if contentWidth >= 4 {
		fieldPadding = 1
	}
	fieldWidth := max(1, contentWidth-2*fieldPadding)

	elements := make([]string, 0, 3)
	headerHeight := 0
	if contentHeight >= 3 {
		title := baseStyle.Foreground(t.Primary()).Bold(true).
			Width(contentWidth).Padding(0, fieldPadding).
			Render(ansi.Truncate("Command Arguments", fieldWidth, "…"))
		elements = append(elements, title)
		headerHeight++
	}
	if contentHeight >= 8 {
		explanation := baseStyle.Foreground(t.Text()).Width(contentWidth).
			Padding(0, fieldPadding).
			Render("This command requires multiple arguments. Please enter values for each:")
		if lipgloss.Height(explanation) <= contentHeight-headerHeight-3 {
			elements = append(elements, explanation)
			headerHeight += lipgloss.Height(explanation)
		}
	}

	inputFields := make([]string, 0, len(m.inputs))
	focusEnd, fieldsHeight := 0, 0
	for i, input := range m.inputs {
		labelStyle := baseStyle.Width(contentWidth).Padding(0, fieldPadding)
		if i == m.focusIndex {
			labelStyle = labelStyle.Foreground(t.Primary()).Bold(true)
		} else {
			labelStyle = labelStyle.Foreground(t.TextMuted())
		}
		label := labelStyle.Render(ansi.Truncate(commandLineText(m.argNames[i])+":", fieldWidth, "…"))
		// Recalculate the local input viewport without moving the edit cursor.
		// textinput reserves one extra column for the cursor at the end.
		input.Width = max(1, fieldWidth-1)
		input.SetCursor(input.Position())
		field := baseStyle.Foreground(t.Text()).Width(contentWidth).
			Padding(0, fieldPadding).
			Render(ansi.Truncate(input.View(), fieldWidth, ""))
		inputFields = append(inputFields, lipgloss.JoinVertical(lipgloss.Left, label, field))
		fieldsHeight += lipgloss.Height(label) + lipgloss.Height(field)
		if i == m.focusIndex {
			focusEnd = fieldsHeight
		}
	}

	// Scroll the fields as focus moves so that the active label and input stay
	// visible, rather than letting a long form push its last fields off-screen.
	if len(inputFields) > 0 {
		lines := strings.Split(lipgloss.JoinVertical(lipgloss.Left, inputFields...), "\n")
		visibleHeight := contentHeight - headerHeight
		start := max(0, focusEnd-visibleHeight)
		end := min(len(lines), start+visibleHeight)
		elements = append(elements, strings.Join(lines[start:end], "\n"))
	}
	return outerStyle.Width(contentWidth + 2*horizontalPadding).
		Render(lipgloss.JoinVertical(lipgloss.Left, elements...))
}

// SetSize sets the size of the component.
func (m *MultiArgumentsDialogCmp) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Bindings implements layout.Bindings.
func (m MultiArgumentsDialogCmp) Bindings() []key.Binding {
	return m.keys.ShortHelp()
}
