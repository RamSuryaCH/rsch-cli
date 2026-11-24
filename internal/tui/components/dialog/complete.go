package dialog

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/logging"
	utilComponents "github.com/opencode-ai/opencode/internal/tui/components/util"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type CompletionItem struct {
	title string
	Title string
	Value string
}

type CompletionItemI interface {
	utilComponents.SimpleListItem
	GetValue() string
	DisplayValue() string
}

func (ci *CompletionItem) Render(selected bool, width int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	itemStyle := baseStyle.
		Width(max(1, width-2)).
		Padding(0, 1)

	if selected {
		itemStyle = itemStyle.
			Background(t.Background()).
			Foreground(t.Primary()).
			Bold(true)
	}

	value := ansi.Truncate(styles.SanitizeTerminalText(ci.GetValue()), max(1, width-4), "…")
	title := itemStyle.Render(styles.SelectionLabel(value, selected))

	return title
}

func (ci *CompletionItem) DisplayValue() string {
	return ci.Title
}

func (ci *CompletionItem) GetValue() string {
	return ci.Value
}

func NewCompletionItem(completionItem CompletionItem) CompletionItemI {
	return &completionItem
}

type CompletionProvider interface {
	GetId() string
	GetEntry() CompletionItemI
	GetChildEntries(query string) ([]CompletionItemI, error)
}

type CompletionSelectedMsg struct {
	SearchString    string
	CompletionValue string
}

type CompletionDialogCompleteItemMsg struct {
	Value string
}

type CompletionDialogCloseMsg struct{}

type CompletionDialog interface {
	tea.Model
	layout.Bindings
	ConsumesKey(msg tea.KeyMsg) bool
	SetWidth(width int)
}

type completionDialogCmp struct {
	query                string
	completionProvider   CompletionProvider
	width                int
	height               int
	pseudoSearchTextArea textarea.Model
	listView             utilComponents.SimpleList[CompletionItemI]
}

type completionDialogKeyMap struct {
	Complete     key.Binding
	Cancel       key.Binding
	PreviousItem key.Binding
	NextItem     key.Binding
}

var completionDialogKeys = completionDialogKeyMap{
	Complete: key.NewBinding(
		key.WithKeys("tab", "enter"),
	),
	Cancel: key.NewBinding(
		key.WithKeys(" ", "esc", "backspace"),
	),
	PreviousItem: key.NewBinding(
		key.WithKeys("up"),
	),
	NextItem: key.NewBinding(
		key.WithKeys("down"),
	),
}

func (c *completionDialogCmp) Init() tea.Cmd {
	return nil
}

func (c *completionDialogCmp) complete(item CompletionItemI) tea.Cmd {
	value := c.pseudoSearchTextArea.Value()

	if value == "" {
		return nil
	}

	return tea.Batch(
		util.CmdHandler(CompletionSelectedMsg{
			SearchString:    value,
			CompletionValue: item.GetValue(),
		}),
		c.close(),
	)
}

func (c *completionDialogCmp) close() tea.Cmd {
	c.listView.SetItems([]CompletionItemI{})
	c.query = ""
	c.pseudoSearchTextArea.Reset()
	c.pseudoSearchTextArea.Blur()

	return util.CmdHandler(CompletionDialogCloseMsg{})
}

func (c *completionDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if c.pseudoSearchTextArea.Focused() {
			// Selection keys belong only to the list. Forwarding them to the
			// hidden textarea also moves its cursor on wrapped query lines.
			if key.Matches(msg, completionDialogKeys.PreviousItem, completionDialogKeys.NextItem) {
				updated, cmd := c.listView.Update(msg)
				c.listView = updated.(utilComponents.SimpleList[CompletionItemI])
				return c, cmd
			}

			if !key.Matches(msg, completionDialogKeys.Complete) {

				var cmd tea.Cmd
				c.pseudoSearchTextArea, cmd = c.pseudoSearchTextArea.Update(msg)
				cmds = append(cmds, cmd)

				var query string
				query = c.pseudoSearchTextArea.Value()
				if query != "" {
					query = query[1:]
				}

				if query != c.query {
					logging.Info("Query", query)
					items, err := c.completionProvider.GetChildEntries(query)
					if err != nil {
						logging.Error("Failed to get child entries", err)
					}

					c.listView.SetItems(items)
					c.query = query
				}

				u, cmd := c.listView.Update(msg)
				c.listView = u.(utilComponents.SimpleList[CompletionItemI])

				cmds = append(cmds, cmd)
			}

			switch {
			case key.Matches(msg, completionDialogKeys.Complete):
				item, i := c.listView.GetSelectedItem()
				if i == -1 {
					return c, nil
				}

				cmd := c.complete(item)

				return c, cmd
			case key.Matches(msg, completionDialogKeys.Cancel):
				// Only close on backspace when there are no characters left
				if msg.String() != "backspace" || len(c.pseudoSearchTextArea.Value()) <= 0 {
					return c, c.close()
				}
			}

			return c, tea.Batch(cmds...)
		} else {
			items, err := c.completionProvider.GetChildEntries("")
			if err != nil {
				logging.Error("Failed to get child entries", err)
			}

			c.listView.SetItems(items)
			c.query = ""
			c.pseudoSearchTextArea.SetValue(msg.String())
			return c, c.pseudoSearchTextArea.Focus()
		}
	case tea.WindowSizeMsg:
		c.width = msg.Width
		c.height = msg.Height
	}

	return c, tea.Batch(cmds...)
}

// ConsumesKey reports keys that exclusively control completion selection. Text
// editing keys continue to the composer so its visible token stays in sync
// with the dialog's query, while list navigation never moves the draft cursor.
func (c *completionDialogCmp) ConsumesKey(msg tea.KeyMsg) bool {
	return key.Matches(
		msg,
		completionDialogKeys.Complete,
		completionDialogKeys.PreviousItem,
		completionDialogKeys.NextItem,
	)
}

func (c *completionDialogCmp) View() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	availableWidth := max(1, c.width)
	maxWidth := min(40, availableWidth)

	completions := c.listView.GetItems()

	for _, cmd := range completions {
		title := cmd.DisplayValue()
		if titleWidth := lipgloss.Width(styles.SanitizeTerminalText(title)); titleWidth > maxWidth-4 {
			maxWidth = min(availableWidth, titleWidth+4)
		}
	}

	c.listView.SetMaxWidth(maxWidth)
	if c.height > 0 {
		c.listView.SetMaxVisibleItems(max(1, min(7, c.height-2)))
	}

	return baseStyle.Padding(0, 0).
		Border(lipgloss.NormalBorder()).
		BorderBottom(false).
		BorderRight(false).
		BorderLeft(false).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(availableWidth).
		Render(c.listView.View())
}

func (c *completionDialogCmp) SetWidth(width int) {
	c.width = width
}

func (c *completionDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(completionDialogKeys)
}

func NewCompletionDialogCmp(completionProvider CompletionProvider) CompletionDialog {
	ti := textarea.New()

	items, err := completionProvider.GetChildEntries("")
	if err != nil {
		logging.Error("Failed to get child entries", err)
	}

	li := utilComponents.NewSimpleList(
		items,
		7,
		"No file matches found",
		false,
	)

	return &completionDialogCmp{
		query:                "",
		completionProvider:   completionProvider,
		pseudoSearchTextArea: ti,
		listView:             li,
	}
}
