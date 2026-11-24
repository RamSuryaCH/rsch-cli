package logs

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type TableComponent interface {
	tea.Model
	layout.Sizeable
	layout.Bindings
	SetFocused(bool)
	IsFocused() bool
}

type tableCmp struct {
	table    table.Model
	focused  bool
	listLogs func() []logging.LogMessage
	logs     []logging.LogMessage
}

type selectedLogMsg logging.LogMessage

func (i *tableCmp) Init() tea.Cmd {
	i.setRows()
	return i.selectionCmd()
}

func (i *tableCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg.(type) {
	case pubsub.Event[logging.LogMessage]:
		i.setRows()
		return i, i.selectionCmd()
	}
	previous := i.selectedLog()
	t, cmd := i.table.Update(msg)
	cmds = append(cmds, cmd)
	i.table = t
	if selected := i.selectedLog(); selected.ID != previous.ID {
		cmds = append(cmds, i.selectionCmd())
	}
	return i, tea.Batch(cmds...)
}

func (i *tableCmp) selectedLog() logging.LogMessage {
	cursor := i.table.Cursor()
	if cursor < 0 || cursor >= len(i.logs) {
		return logging.LogMessage{}
	}
	return i.logs[cursor]
}

func (i *tableCmp) selectionCmd() tea.Cmd {
	return util.CmdHandler(selectedLogMsg(i.selectedLog()))
}

func (i *tableCmp) View() string {
	t := theme.CurrentTheme()
	defaultStyles := table.DefaultStyles()
	defaultStyles.Selected = defaultStyles.Selected.Foreground(t.Primary())
	if i.focused {
		defaultStyles.Header = defaultStyles.Header.Foreground(t.Primary()).Bold(true)
	} else {
		defaultStyles.Header = defaultStyles.Header.Foreground(t.TextMuted())
	}
	i.table.SetStyles(defaultStyles)
	return styles.ForceReplaceBackgroundWithLipgloss(i.table.View(), t.Background())
}

func (i *tableCmp) GetSize() (int, int) {
	return i.table.Width(), i.table.Height()
}

func (i *tableCmp) SetSize(width int, height int) tea.Cmd {
	i.table.SetWidth(width)
	i.table.SetHeight(height)
	columns := i.table.Columns()
	for idx, column := range columns {
		column.Width = max(1, (width/len(columns))-2)
		columns[idx] = column
	}
	i.table.SetColumns(columns)
	return nil
}

func (i *tableCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(i.table.KeyMap)
}

func (i *tableCmp) SetFocused(focused bool) {
	i.focused = focused
	if focused {
		i.table.Focus()
	} else {
		i.table.Blur()
	}

	columns := i.table.Columns()
	if len(columns) > 0 {
		// The marker makes pane focus visible even when terminal colors are
		// disabled. Keep it in the existing header so it consumes no extra rows.
		columns[0].Title = styles.SelectionLabel("ID", focused)
		i.table.SetColumns(columns)
	}
}

func (i *tableCmp) IsFocused() bool {
	return i.focused
}

func (i *tableCmp) setRows() {
	rows := []table.Row{}
	previous := i.selectedLog()
	// Keep the displayed rows and their original identities in one snapshot.
	// Display sanitization must never be used to identify a selected record.
	logs := append([]logging.LogMessage(nil), i.listLogs()...)
	slices.SortStableFunc(logs, func(a, b logging.LogMessage) int {
		if a.Time.Before(b.Time) {
			return 1
		}
		if a.Time.After(b.Time) {
			return -1
		}
		return 0
	})

	for _, log := range logs {
		rows = append(rows, logTableRow(log))
	}
	i.logs = logs
	i.table.SetRows(rows)
	for index, log := range logs {
		if log.ID == previous.ID {
			i.table.SetCursor(index)
			break
		}
	}
}

func logTableRow(log logging.LogMessage) table.Row {
	log = sanitizeLog(log)
	attributes, _ := json.Marshal(log.Attributes)
	return table.Row{
		sanitizeTableCell(log.ID),
		log.Time.Format("15:04:05"),
		sanitizeTableCell(log.Level),
		sanitizeTableCell(log.Message),
		sanitizeTableCell(string(attributes)),
	}
}

func sanitizeLog(log logging.LogMessage) logging.LogMessage {
	log.ID = styles.SanitizeTerminalText(log.ID)
	log.Level = styles.SanitizeTerminalText(log.Level)
	log.Message = styles.SanitizeTerminalText(log.Message)
	attributes := make([]logging.Attr, len(log.Attributes))
	for idx, attribute := range log.Attributes {
		attributes[idx] = logging.Attr{
			Key:   styles.SanitizeTerminalText(attribute.Key),
			Value: styles.SanitizeTerminalText(attribute.Value),
		}
	}
	log.Attributes = attributes
	return log
}

func sanitizeTableCell(value string) string {
	return strings.Join(strings.Fields(styles.SanitizeTerminalText(value)), " ")
}

func NewLogsTable() TableComponent {
	columns := []table.Column{
		{Title: "ID", Width: 4},
		{Title: "Time", Width: 4},
		{Title: "Level", Width: 10},
		{Title: "Message", Width: 10},
		{Title: "Attributes", Width: 10},
	}

	tableModel := table.New(
		table.WithColumns(columns),
	)
	component := &tableCmp{table: tableModel, listLogs: logging.List}
	component.SetFocused(true)
	return component
}
