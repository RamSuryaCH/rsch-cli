package page

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/tui/components/logs"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

var LogsPage PageID = "logs"

type logsPane uint8

const (
	logsTablePane logsPane = iota
	logsDetailsPane
)

var logsPageKeys = struct {
	SwitchPane key.Binding
}{
	SwitchPane: key.NewBinding(
		key.WithKeys("tab", "shift+tab"),
		key.WithHelp("tab/shift+tab", "switch log pane"),
	),
}

type LogPage interface {
	tea.Model
	layout.Sizeable
	layout.Bindings
}
type logsPage struct {
	width, height   int
	table           layout.Container
	details         layout.Container
	tableComponent  logs.TableComponent
	detailComponent logs.DetailComponent
	focusedPane     logsPane
}

func (p *logsPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, p.SetSize(msg.Width, msg.Height)
	case tea.KeyMsg:
		if key.Matches(msg, logsPageKeys.SwitchPane) {
			if p.focusedPane == logsTablePane {
				p.focusedPane = logsDetailsPane
			} else {
				p.focusedPane = logsTablePane
			}
			p.syncFocus()
			return p, nil
		}

		// Only the visibly focused pane receives keyboard input. This prevents a
		// scroll command from moving both the table selection and detail viewport.
		if p.focusedPane == logsDetailsPane {
			details, cmd := p.details.Update(msg)
			p.details = details.(layout.Container)
			return p, cmd
		}
		table, cmd := p.table.Update(msg)
		p.table = table.(layout.Container)
		return p, cmd
	}

	table, cmd := p.table.Update(msg)
	cmds = append(cmds, cmd)
	p.table = table.(layout.Container)
	details, cmd := p.details.Update(msg)
	cmds = append(cmds, cmd)
	p.details = details.(layout.Container)

	return p, tea.Batch(cmds...)
}

func (p *logsPage) View() string {
	style := styles.BaseStyle().Width(p.width).Height(p.height)
	return style.Render(lipgloss.JoinVertical(lipgloss.Top,
		p.table.View(),
		p.details.View(),
	))
}

func (p *logsPage) BindingKeys() []key.Binding {
	bindings := []key.Binding{logsPageKeys.SwitchPane}
	bindings = append(bindings, p.table.BindingKeys()...)
	bindings = append(bindings, p.details.BindingKeys()...)
	return bindings
}

// GetSize implements LogPage.
func (p *logsPage) GetSize() (int, int) {
	return p.width, p.height
}

// SetSize implements LogPage.
func (p *logsPage) SetSize(width int, height int) tea.Cmd {
	p.width = width
	p.height = height
	return tea.Batch(
		p.table.SetSize(width, height/2),
		p.details.SetSize(width, height/2),
	)
}

func (p *logsPage) Init() tea.Cmd {
	return tea.Batch(
		p.table.Init(),
		p.details.Init(),
	)
}

func NewLogsPage() LogPage {
	tableComponent := logs.NewLogsTable()
	detailComponent := logs.NewLogsDetails()
	page := &logsPage{
		table:           layout.NewContainer(tableComponent, layout.WithBorderAll()),
		details:         layout.NewContainer(detailComponent, layout.WithBorderAll()),
		tableComponent:  tableComponent,
		detailComponent: detailComponent,
		focusedPane:     logsTablePane,
	}
	page.syncFocus()
	return page
}

func (p *logsPage) syncFocus() {
	p.tableComponent.SetFocused(p.focusedPane == logsTablePane)
	p.detailComponent.SetFocused(p.focusedPane == logsDetailsPane)
}
