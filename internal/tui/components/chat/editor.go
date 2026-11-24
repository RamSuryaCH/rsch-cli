package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/process"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type editorCmp struct {
	width              int
	height             int
	app                *app.App
	session            session.Session
	textarea           textarea.Model
	attachments        []message.Attachment
	deleteMode         bool
	sendPending        bool
	pendingText        string
	pendingAttachments []message.Attachment
	sendError          string
}

// SendResultMsg acknowledges whether the chat page accepted the current draft.
// The editor deliberately keeps the draft until this message arrives so a
// session or agent startup failure never destroys user input.
type SendResultMsg struct {
	Text        string
	Attachments []message.Attachment
	Accepted    bool
	Err         error
}

type externalEditorResultMsg struct {
	content string
	err     error
}

type EditorKeyMaps struct {
	Send       key.Binding
	Newline    key.Binding
	OpenEditor key.Binding
}

type bluredEditorKeyMaps struct {
	Send       key.Binding
	Focus      key.Binding
	OpenEditor key.Binding
}
type DeleteAttachmentKeyMaps struct {
	AttachmentDeleteMode key.Binding
	Escape               key.Binding
	DeleteAllAttachments key.Binding
}

// terminalProcess adapts the shared process boundary to Bubble Tea's
// terminal-release interface. The editor remains interactive, but process
// creation, argv handling, cancellation, and exit semantics stay centralized.
type terminalProcess struct {
	spec process.Spec
}

func (p *terminalProcess) Run() error {
	_, err := process.Run(context.Background(), p.spec)
	return err
}
func (p *terminalProcess) SetStdin(value io.Reader) {
	if p.spec.Stdin == nil {
		p.spec.Stdin = value
	}
}
func (p *terminalProcess) SetStdout(value io.Writer) {
	if p.spec.Stdout == nil {
		p.spec.Stdout = value
	}
}
func (p *terminalProcess) SetStderr(value io.Writer) {
	if p.spec.Stderr == nil {
		p.spec.Stderr = value
	}
}

var editorMaps = EditorKeyMaps{
	Send: key.NewBinding(
		key.WithKeys("enter", "ctrl+s"),
		key.WithHelp("enter/ctrl+s", "send message"),
	),
	Newline: key.NewBinding(
		key.WithKeys("ctrl+j"),
		key.WithHelp("ctrl+j", "new line"),
	),
	OpenEditor: key.NewBinding(
		key.WithKeys("alt+e"),
		key.WithHelp("alt+e", "open editor"),
	),
}

var DeleteKeyMaps = DeleteAttachmentKeyMaps{
	AttachmentDeleteMode: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r+{i}", "delete attachment at index i"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel delete mode"),
	),
	DeleteAllAttachments: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("ctrl+r+r", "delete all attachments"),
	),
}

const (
	maxAttachments = 5
)

func (m *editorCmp) openEditor() tea.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "nvim"
	}
	argv := strings.Fields(editor)
	if len(argv) == 0 {
		return util.ReportError(fmt.Errorf("editor command is empty"))
	}

	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	path := tmpfile.Name()
	if _, err := tmpfile.WriteString(m.textarea.Value()); err != nil {
		_ = tmpfile.Close()
		_ = os.Remove(path)
		return util.ReportError(err)
	}
	if err := tmpfile.Close(); err != nil {
		_ = os.Remove(path)
		return util.ReportError(err)
	}
	c := &terminalProcess{spec: process.Spec{Argv: append(argv, path)}}
	return tea.Exec(c, func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return externalEditorResultMsg{err: err}
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return externalEditorResultMsg{err: err}
		}
		if len(content) == 0 {
			return externalEditorResultMsg{content: ""}
		}
		return externalEditorResultMsg{content: string(content)}
	})
}

func (m *editorCmp) Init() tea.Cmd {
	return textarea.Blink
}

func (m *editorCmp) send() tea.Cmd {
	if m.sendPending {
		return util.ReportWarn("The current draft is still being submitted...")
	}
	if m.app.CoderAgent.IsSessionBusy(m.session.ID) {
		return util.ReportWarn("Agent is working, please wait...")
	}

	value := m.textarea.Value()
	if value == "" && len(m.attachments) == 0 {
		return nil
	}
	m.sendPending = true
	m.pendingText = value
	m.pendingAttachments = cloneAttachments(m.attachments)
	m.sendError = ""
	return tea.Batch(
		util.CmdHandler(SendMsg{
			Text:        value,
			Attachments: cloneAttachments(m.attachments),
		}),
	)
}

func cloneAttachments(items []message.Attachment) []message.Attachment {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]message.Attachment, len(items))
	for i, item := range items {
		cloned[i] = item
	}
	return cloned
}

func attachmentsEqual(left, right []message.Attachment) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].FilePath != right[i].FilePath ||
			left[i].FileName != right[i].FileName ||
			left[i].MimeType != right[i].MimeType ||
			!bytes.Equal(left[i].Content, right[i].Content) {
			return false
		}
	}
	return true
}

func (m *editorCmp) applySendResult(result SendResultMsg) {
	if !m.sendPending {
		return
	}
	if result.Text != m.pendingText || !attachmentsEqual(result.Attachments, m.pendingAttachments) {
		return
	}
	m.sendPending = false
	if result.Accepted {
		// A user may continue editing while the acceptance command is in flight.
		// Clear only the exact submitted snapshot and preserve newer edits.
		if m.textarea.Value() == m.pendingText {
			m.textarea.Reset()
		}
		if attachmentsEqual(m.attachments, m.pendingAttachments) {
			m.attachments = nil
		}
		m.sendError = ""
	} else if result.Err != nil {
		m.sendError = result.Err.Error()
	} else {
		m.sendError = "the run was not accepted"
	}
	m.pendingText = ""
	m.pendingAttachments = nil
}

func (m *editorCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case dialog.ThemeChangedMsg:
		m.textarea = CreateTextArea(&m.textarea)
	case SendResultMsg:
		m.applySendResult(msg)
		return m, nil
	case externalEditorResultMsg:
		if msg.err != nil {
			return m, util.ReportError(msg.err)
		}
		m.textarea.SetValue(msg.content)
		m.sendError = ""
		return m, nil
	case dialog.CompletionSelectedMsg:
		m.applyCompletion(msg.SearchString, msg.CompletionValue)
		return m, nil
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			m.session = msg
		}
		return m, nil
	case SessionClearedMsg:
		m.session = session.Session{}
		m.sendPending = false
		m.pendingText = ""
		m.pendingAttachments = nil
		return m, nil
	case dialog.AttachmentAddedMsg:
		if len(m.attachments) >= maxAttachments {
			logging.ErrorPersist(fmt.Sprintf("cannot add more than %d images", maxAttachments))
			return m, cmd
		}
		m.attachments = append(m.attachments, msg.Attachment)
	case tea.KeyMsg:
		if key.Matches(msg, DeleteKeyMaps.AttachmentDeleteMode) {
			m.deleteMode = true
			return m, nil
		}
		if key.Matches(msg, DeleteKeyMaps.DeleteAllAttachments) && m.deleteMode {
			m.deleteMode = false
			m.attachments = nil
			return m, nil
		}
		if m.deleteMode && len(msg.Runes) > 0 && unicode.IsDigit(msg.Runes[0]) {
			num := int(msg.Runes[0] - '0')
			m.deleteMode = false
			if num < 10 && len(m.attachments) > num {
				if num == 0 {
					m.attachments = m.attachments[num+1:]
				} else {
					m.attachments = slices.Delete(m.attachments, num, num+1)
				}
				return m, nil
			}
		}
		if key.Matches(msg, messageKeys.PageUp) || key.Matches(msg, messageKeys.PageDown) ||
			key.Matches(msg, messageKeys.HalfPageUp) || key.Matches(msg, messageKeys.HalfPageDown) {
			return m, nil
		}
		if key.Matches(msg, editorMaps.OpenEditor) {
			if m.app.CoderAgent.IsSessionBusy(m.session.ID) {
				return m, util.ReportWarn("Agent is working, please wait...")
			}
			return m, m.openEditor()
		}
		if m.textarea.Focused() && key.Matches(msg, editorMaps.Newline) {
			m.textarea.InsertString("\n")
			return m, nil
		}
		if key.Matches(msg, DeleteKeyMaps.Escape) {
			m.deleteMode = false
			return m, nil
		}
		// Hanlde Enter key
		if m.textarea.Focused() && key.Matches(msg, editorMaps.Send) {
			value := m.textarea.Value()
			if len(value) > 0 && value[len(value)-1] == '\\' {
				// If the last character is a backslash, remove it and add a newline
				m.textarea.SetValue(value[:len(value)-1] + "\n")
				return m, nil
			} else {
				// Otherwise, send the message
				return m, m.send()
			}
		}

	}
	m.textarea, cmd = m.textarea.Update(msg)
	if m.textarea.Value() == "/" {
		m.textarea.Reset()
		return m, tea.Batch(cmd, util.CmdHandler(dialog.ShowCommandDialogMsg{}))
	}
	return m, cmd
}

func (m *editorCmp) applyCompletion(search, replacement string) {
	if search == "" || strings.Contains(search, "\n") {
		return
	}
	lines := strings.Split(m.textarea.Value(), "\n")
	row := m.textarea.Line()
	line := []rune(lines[row])
	info := m.textarea.LineInfo()
	cursor := info.StartColumn + info.ColumnOffset
	searchLength := len([]rune(search))

	// Complete the occurrence containing the edit cursor, rather than the
	// first occurrence anywhere in the draft. Ignore stale results after the
	// user has moved away from the token. Column offsets are rune indices.
	for start := cursor; start >= max(0, cursor-searchLength); start-- {
		end := start + searchLength
		if end > len(line) || string(line[start:end]) != search {
			continue
		}
		lines[row] = string(line[:start]) + replacement + string(line[end:])
		m.textarea.SetValue(strings.Join(lines, "\n"))
		cursorRow := row + strings.Count(replacement, "\n")
		cursorColumn := start + len([]rune(replacement))
		if lastNewline := strings.LastIndex(replacement, "\n"); lastNewline >= 0 {
			cursorColumn = len([]rune(replacement[lastNewline+1:]))
		}
		// SetValue moves to the end. Move by logical lines before restoring the
		// insertion point so trailing prose and soft wraps do not move it.
		for m.textarea.Line() > cursorRow {
			m.textarea.CursorStart()
			m.textarea.CursorUp()
		}
		m.textarea.SetCursor(cursorColumn)
		return
	}
}

func (m *editorCmp) View() string {
	t := theme.CurrentTheme()

	// Style the prompt with theme colors
	style := lipgloss.NewStyle().
		Padding(0, 1, 0, 0).
		Bold(true).
		Foreground(t.Primary())

	extraLines := 0
	if len(m.attachments) > 0 {
		extraLines++
	}
	if m.sendError != "" {
		extraLines++
	}
	m.textarea.SetHeight(max(1, m.height-extraLines))
	parts := make([]string, 0, 3)
	if len(m.attachments) > 0 {
		parts = append(parts, m.attachmentsContent())
	}
	parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Top, style.Render("›"), m.textarea.View()))
	if m.sendError != "" {
		detail := strings.Join(strings.Fields(m.sendError), " ")
		text := "Send failed: " + detail + " · draft preserved; press enter to retry"
		text = ansi.Truncate(text, max(1, m.width), "…")
		parts = append(parts, styles.BaseStyle().Foreground(t.Error()).Render(text))
	}
	return lipgloss.JoinVertical(lipgloss.Top, parts...)
}

func (m *editorCmp) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	m.textarea.SetWidth(max(1, width-3)) // account for the prompt and padding
	m.textarea.SetHeight(max(1, height))
	return nil
}

func (m *editorCmp) GetSize() (int, int) {
	return m.textarea.Width(), m.textarea.Height()
}

func (m *editorCmp) attachmentsContent() string {
	var styledAttachments []string
	t := theme.CurrentTheme()
	attachmentStyles := styles.BaseStyle().
		MarginLeft(1).
		Background(t.BackgroundSecondary()).
		Foreground(t.Text())
	for i, attachment := range m.attachments {
		name := styles.SanitizeTerminalText(attachment.FileName)
		filename := fmt.Sprintf(" %s %s", styles.DocumentIcon, ansi.Truncate(name, 12, "…"))
		if m.deleteMode {
			filename = fmt.Sprintf("%d%s", i, filename)
		}
		styledAttachments = append(styledAttachments, attachmentStyles.Render(filename))
	}
	content := lipgloss.JoinHorizontal(lipgloss.Left, styledAttachments...)
	return content
}

func (m *editorCmp) BindingKeys() []key.Binding {
	bindings := []key.Binding{}
	bindings = append(bindings, layout.KeyMapToSlice(editorMaps)...)
	bindings = append(bindings, layout.KeyMapToSlice(DeleteKeyMaps)...)
	return bindings
}

func CreateTextArea(existing *textarea.Model) textarea.Model {
	t := theme.CurrentTheme()
	bgColor := t.Background()
	textColor := t.Text()
	textMutedColor := t.TextMuted()

	ta := textarea.New()
	if existing != nil {
		// Restyle the existing model without resetting its cursor, viewport,
		// focus, or key bindings while the user is editing a draft.
		ta = *existing
	}
	ta.BlurredStyle.Base = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	ta.BlurredStyle.CursorLine = styles.BaseStyle().Background(bgColor)
	ta.BlurredStyle.Placeholder = styles.BaseStyle().Background(bgColor).Foreground(textMutedColor)
	ta.BlurredStyle.Text = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	ta.FocusedStyle.Base = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	ta.FocusedStyle.CursorLine = styles.BaseStyle().Background(bgColor)
	ta.FocusedStyle.Placeholder = styles.BaseStyle().Background(bgColor).Foreground(textMutedColor)
	ta.FocusedStyle.Text = styles.BaseStyle().Background(bgColor).Foreground(textColor)

	ta.Prompt = " "
	ta.Placeholder = "Ask RSCH to inspect, explain, edit, or test…"
	ta.ShowLineNumbers = false
	ta.CharLimit = -1

	// Refresh textarea's internal active-style pointer after changing styles.
	if existing == nil || existing.Focused() {
		ta.Focus()
	} else {
		ta.Blur()
	}
	return ta
}

func NewEditorCmp(app *app.App) tea.Model {
	ta := CreateTextArea(nil)
	return &editorCmp{
		app:      app,
		textarea: ta,
	}
}
