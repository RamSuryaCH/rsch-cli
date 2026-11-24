package page

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/completions"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/sandbox"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

var ChatPage PageID = "chat"

type chatPage struct {
	app                  *app.App
	width, height        int
	editor               layout.Container
	messages             layout.Container
	sidebar              layout.Container
	layout               layout.SplitPaneLayout
	session              session.Session
	completionDialog     dialog.CompletionDialog
	showCompletionDialog bool
	runNotices           map[string]runNotice
	sidebarInitialized   bool
}

type runNotice struct {
	canceled bool
	message  string
}

type runFinishedMsg struct {
	sessionID string
	err       error
}

type ChatKeyMap struct {
	ShowCompletionDialog key.Binding
	NewSession           key.Binding
	Cancel               key.Binding
}

var keyMap = ChatKeyMap{
	ShowCompletionDialog: key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "Complete"),
	),
	NewSession: key.NewBinding(
		key.WithKeys("alt+n"),
		key.WithHelp("alt+n", "new session"),
	),
	Cancel: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	),
}

func (p *chatPage) Init() tea.Cmd {
	cmds := []tea.Cmd{
		p.layout.Init(),
		p.completionDialog.Init(),
	}
	return tea.Batch(cmds...)
}

func (p *chatPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cmd := p.SetSize(msg.Width, msg.Height)
		cmds = append(cmds, cmd)
		return p, tea.Batch(cmds...)
	case dialog.CompletionDialogCloseMsg:
		p.showCompletionDialog = false
	case chat.SendMsg:
		cmd, err := p.sendMessage(msg.Text, msg.Attachments)
		result := util.CmdHandler(chat.SendResultMsg{
			Text:        msg.Text,
			Attachments: msg.Attachments,
			Accepted:    err == nil,
			Err:         err,
		})
		if err != nil {
			return p, tea.Batch(cmd, result, util.ReportError(err))
		}
		return p, tea.Batch(cmd, result)
	case runFinishedMsg:
		p.applyRunResult(msg)
		return p, nil
	case dialog.CommandRunCustomMsg:
		// Check if the agent is busy before executing custom commands
		if p.app.CoderAgent.IsBusy() {
			return p, util.ReportWarn("Agent is busy, please wait before executing a command...")
		}

		// Handle custom command execution
		cmd, err := p.sendMessage(msg.Content, nil)
		if err != nil {
			return p, tea.Batch(cmd, util.ReportError(err))
		}
		return p, cmd
	case chat.SessionSelectedMsg:
		hadSession := p.session.ID != ""
		p.session = msg
		if !hadSession {
			cmd := p.setSidebar()
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	case chat.SessionClearedMsg:
		p.session = session.Session{}
		p.showCompletionDialog = false
		cmds = append(cmds, p.clearSidebar())
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent && p.session.ID != "" && msg.Payload.ID == p.session.ID {
			p.session = msg.Payload
		}
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keyMap.Cancel) && p.showCompletionDialog:
			context, contextCmd := p.completionDialog.Update(msg)
			p.completionDialog = context.(dialog.CompletionDialog)
			p.showCompletionDialog = false
			return p, contextCmd
		case key.Matches(msg, keyMap.ShowCompletionDialog):
			p.showCompletionDialog = true
			// Continue sending keys to layout->chat
		case key.Matches(msg, keyMap.NewSession):
			p.session = session.Session{}
			return p, tea.Batch(
				p.clearSidebar(),
				util.CmdHandler(chat.SessionClearedMsg{}),
			)
		case key.Matches(msg, keyMap.Cancel):
			if p.session.ID != "" && p.app.CoderAgent.IsSessionBusy(p.session.ID) {
				// Cancel the current session's generation process
				// This allows users to interrupt long-running operations
				p.app.CoderAgent.Cancel(p.session.ID)
				return p, nil
			}
		}
	}
	if p.showCompletionDialog {
		context, contextCmd := p.completionDialog.Update(msg)
		p.completionDialog = context.(dialog.CompletionDialog)
		cmds = append(cmds, contextCmd)

		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			if p.completionDialog.ConsumesKey(keyMsg) {
				return p, tea.Batch(cmds...)
			}
		}
	}

	// A hidden sidebar retains its subscription and session state. Keep
	// delivering asynchronous messages while it is detached from the layout.
	if _, isKey := msg.(tea.KeyMsg); !isKey && p.sidebar != nil && !p.sidebarVisible() {
		u, sidebarCmd := p.sidebar.Update(msg)
		p.sidebar = u.(layout.Container)
		cmds = append(cmds, sidebarCmd)
	}
	u, cmd := p.layout.Update(msg)
	cmds = append(cmds, cmd)
	p.layout = u.(layout.SplitPaneLayout)

	return p, tea.Batch(cmds...)
}

func (p *chatPage) setSidebar() tea.Cmd {
	if p.sidebar == nil {
		p.sidebar = layout.NewContainer(
			chat.NewSidebarCmp(p.session, p.app.History),
			layout.WithPadding(1, 1, 1, 1),
			layout.WithBorder(false, false, false, true),
		)
	}
	if !p.sidebarVisible() {
		return p.layout.ClearRightPanel()
	}
	if !p.sidebarInitialized {
		p.sidebarInitialized = true
		return tea.Batch(p.layout.SetRightPanel(p.sidebar), p.sidebar.Init())
	}
	return p.layout.SetRightPanel(p.sidebar)
}

func (p *chatPage) sidebarVisible() bool {
	return p.session.ID != "" && layout.SidebarVisible(p.width)
}

func (p *chatPage) clearSidebar() tea.Cmd {
	return p.layout.ClearRightPanel()
}

func (p *chatPage) sendMessage(text string, attachments []message.Attachment) (tea.Cmd, error) {
	trimmed := strings.TrimSpace(text)
	if prompt, ok := expandAgentSlashCommand(trimmed); ok {
		return p.startAgentRun(prompt, attachments)
	}
	if cmd, handled := p.handleSlashCommand(trimmed); handled {
		return cmd, nil
	}
	if strings.HasPrefix(trimmed, "/") {
		name := strings.Fields(trimmed)[0]
		return nil, fmt.Errorf("unknown slash command %q; press / to browse commands", name)
	}
	return p.startAgentRun(text, attachments)
}

func (p *chatPage) startAgentRun(text string, attachments []message.Attachment) (tea.Cmd, error) {
	var cmds []tea.Cmd
	p.ensureRunNotices()
	delete(p.runNotices, p.session.ID)
	if p.session.ID == "" {
		session, err := p.app.Sessions.Create(context.Background(), "New Session")
		if err != nil {
			p.runNotices[""] = runNotice{message: err.Error()}
			return nil, err
		}

		p.session = session
		cmd := p.setSidebar()
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, util.CmdHandler(chat.SessionSelectedMsg(session)))
	}

	done, err := p.app.CoderAgent.Run(context.Background(), p.session.ID, text, attachments...)
	if err != nil {
		p.runNotices[p.session.ID] = runNotice{message: err.Error()}
		return tea.Batch(cmds...), err
	}
	if done == nil {
		err = errors.New("agent run was accepted without a completion channel")
		p.runNotices[p.session.ID] = runNotice{message: err.Error()}
		return tea.Batch(cmds...), err
	}
	sessionID := p.session.ID
	cmds = append(cmds, waitForRun(sessionID, done))
	return tea.Batch(cmds...), nil
}

func waitForRun(sessionID string, done <-chan agent.AgentEvent) tea.Cmd {
	return func() tea.Msg {
		result, ok := <-done
		if !ok {
			return runFinishedMsg{sessionID: sessionID, err: errors.New("agent run ended without a result")}
		}
		return runFinishedMsg{sessionID: sessionID, err: result.Error}
	}
}

func (p *chatPage) ensureRunNotices() {
	if p.runNotices == nil {
		p.runNotices = make(map[string]runNotice)
	}
}

func (p *chatPage) applyRunResult(result runFinishedMsg) {
	p.ensureRunNotices()
	if result.err == nil {
		delete(p.runNotices, result.sessionID)
		return
	}
	p.runNotices[result.sessionID] = runNotice{
		canceled: errors.Is(result.err, context.Canceled) || errors.Is(result.err, agent.ErrRequestCancelled),
		message:  result.err.Error(),
	}

}

func (p *chatPage) renderRunNotice(width int) string {
	p.ensureRunNotices()
	notice, ok := p.runNotices[p.session.ID]
	if !ok || notice.message == "" {
		return ""
	}
	t := theme.CurrentTheme()
	color := t.Error()
	label := "Run failed"
	if notice.canceled {
		color = t.Warning()
		label = "Run canceled"
	}
	detail := strings.Join(strings.Fields(notice.message), " ")
	text := fmt.Sprintf("%s: %s · send again to retry", label, detail)
	return styles.BaseStyle().Foreground(color).Render(ansi.Truncate(text, max(1, width), "…"))
}

func (p *chatPage) handleSlashCommand(text string) (tea.Cmd, bool) {
	if !strings.HasPrefix(text, "/") {
		return nil, false
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil, true
	}
	name := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	switch name {
	case "help":
		return util.CmdHandler(dialog.ShowHelpDialogMsg{}), true
	case "status":
		goals := p.app.Workflow.Goals()
		tasks := p.app.Workflow.Tasks()
		completed, blocked := 0, 0
		for _, item := range tasks {
			switch item.Status {
			case engineering.StatusCompleted, engineering.StatusVerified, engineering.StatusPassed:
				completed++
			case engineering.StatusBlocked, engineering.StatusFailed:
				blocked++
			}
		}
		cost := 0.0
		if sessions, err := p.app.Sessions.List(context.Background()); err == nil {
			for _, item := range sessions {
				cost += item.Cost
			}
		}
		latestGoal := "none"
		if len(goals) > 0 {
			latestGoal = fmt.Sprintf("%s [%s]", goals[len(goals)-1].Title, goals[len(goals)-1].Status)
		}
		return util.ReportInfo(fmt.Sprintf("goal: %s\nprogress: %d/%d tasks (%d%%)\nagents: %d\ntests/verification: task evidence is shown on /tasks show\ncurrent operation: %t\nblocking errors: %d\nrisk: %s\ncost: %.6f", latestGoal, completed, len(tasks), percentage(completed, len(tasks)), len(p.app.Agents.List()), p.app.CoderAgent.IsBusy(), blocked, riskLabel(blocked), cost)), true
	case "tasks":
		items := p.app.Workflow.Tasks()
		if len(items) == 0 {
			return util.ReportInfo("no persisted tasks"), true
		}
		if len(parts) >= 3 && strings.EqualFold(parts[1], "show") {
			for _, item := range items {
				if item.ID == parts[2] {
					return util.ReportInfo(fmt.Sprintf("task %s\nstatus: %s\ngoal: %s\ncommand: %s\ntimeout: %s\nretries: %d\ndependencies: %s\nagent: %s", item.ID, item.Status, item.GoalID, strings.Join(item.Command, " "), item.Timeout, item.RetryLimit, strings.Join(item.Dependencies, ", "), item.Agent)), true
				}
			}
			return util.ReportError(fmt.Errorf("task %q not found", parts[2])), true
		}
		if len(parts) >= 3 {
			if next, ok := taskStatusForCommand(parts[1]); ok {
				item, err := p.app.TransitionTask(context.Background(), parts[2], next)
				if err != nil {
					return util.ReportError(err), true
				}
				return util.ReportInfo(fmt.Sprintf("task %s [%s]", item.ID, item.Status)), true
			}
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			lines = append(lines, fmt.Sprintf("%s [%s] %s", item.ID, item.Status, item.Title))
		}
		return util.ReportInfo(strings.Join(lines, "\n")), true
	case "agents":
		items := p.app.Agents.List()
		if len(items) == 0 {
			return util.ReportInfo("no persisted agents"), true
		}
		if len(parts) >= 3 && strings.EqualFold(parts[1], "show") {
			for _, item := range items {
				if item.ID == parts[2] {
					return util.ReportInfo(fmt.Sprintf("agent %s\nrole: %s\nstatus: %s\ntask: %s\nmodel: %s\nprovider: %s\nskills: %s\ntools: %s\npermissions: %s\nrisk: %s\nbudget: %d\ntimeout: %s\nworktree: %s\ntokens: %d\ncost: %.6f\nduration: %s\nfiles: %s\nerrors: %s\nverification: %s", item.ID, item.Role, item.Status, item.TaskID, item.Model, item.Provider, strings.Join(item.Skills, ", "), strings.Join(item.Tools, ", "), strings.Join(item.Permissions, ", "), item.Risk, item.Budget, item.Timeout, item.Worktree, item.Tokens, item.Cost, item.Duration, strings.Join(item.Files, ", "), strings.Join(item.Errors, "; "), strings.Join(item.Verification, ", "))), true
				}
			}
			return util.ReportError(fmt.Errorf("agent %q not found", parts[2])), true
		}
		if len(parts) >= 3 {
			if next, ok := agentStatusForCommand(parts[1]); ok {
				if err := p.app.Agents.Transition(parts[2], next); err != nil {
					return util.ReportError(err), true
				}
				item, found := p.app.Agents.Get(parts[2])
				if !found {
					return util.ReportError(fmt.Errorf("agent %q not found after transition", parts[2])), true
				}
				return util.ReportInfo(fmt.Sprintf("agent %s [%s]", item.ID, item.Status)), true
			}
		}
		lines := make([]string, 0, len(items))
		for _, item := range items {
			lines = append(lines, fmt.Sprintf("%s [%s] %s task=%s", item.ID, item.Status, item.Role, item.TaskID))
		}
		return util.ReportInfo(strings.Join(lines, "\n")), true
	case "security":
		capability := sandbox.Detect()
		return util.ReportInfo(fmt.Sprintf("%s: %s (%s)", capability.Platform, capability.State, capability.Backend)), true
	}
	return nil, false
}

func expandAgentSlashCommand(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", false
	}
	name := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	known := map[string]bool{"goal": true, "interview": true, "plan": true, "spec": true, "run": true, "context": true, "memory": true, "search": true, "test": true, "fix": true, "verify": true, "review": true, "performance": true, "explain": true, "why": true, "diff": true, "git": true, "checkpoint": true, "rewind": true, "fork": true, "experiment": true, "cost": true}
	if !known[name] {
		return "", false
	}
	prompt := "Execute the RSCH workflow command /" + name
	if len(parts) > 1 {
		prompt += " with arguments: " + strings.Join(parts[1:], " ")
	}
	return prompt, true
}

func taskStatusForCommand(command string) (engineering.Status, bool) {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "retry":
		return engineering.StatusRetrying, true
	case "pause":
		return engineering.StatusPaused, true
	case "resume":
		return engineering.StatusReady, true
	case "cancel":
		return engineering.StatusCanceled, true
	default:
		return "", false
	}
}

func agentStatusForCommand(command string) (orchestration.AgentStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "start", "retry":
		return orchestration.AgentStarting, true
	case "run":
		return orchestration.AgentRunning, true
	case "pause":
		return orchestration.AgentPaused, true
	case "resume":
		return orchestration.AgentResumed, true
	case "cancel":
		return orchestration.AgentCanceled, true
	case "complete":
		return orchestration.AgentCompleted, true
	case "fail":
		return orchestration.AgentFailed, true
	default:
		return "", false
	}
}

func percentage(done, total int) int {
	if total <= 0 {
		return 0
	}
	return done * 100 / total
}

func riskLabel(blocked int) string {
	if blocked > 0 {
		return "blocked"
	}
	return "normal"
}

func (p *chatPage) SetSize(width, height int) tea.Cmd {
	p.width, p.height = width, height
	if !p.sidebarVisible() {
		return tea.Batch(
			p.layout.ClearRightPanel(),
			p.layout.SetSize(width, max(0, height-1)),
		)
	}
	if p.sidebar != nil {
		cmds := []tea.Cmd{p.layout.SetRightPanel(p.sidebar), p.layout.SetSize(width, max(0, height-1))}
		if !p.sidebarInitialized {
			p.sidebarInitialized = true
			cmds = append(cmds, p.sidebar.Init())
		}
		return tea.Batch(cmds...)
	}
	return p.layout.SetSize(width, max(0, height-1))
}

func (p *chatPage) GetSize() (int, int) {
	return p.layout.GetSize()
}

func (p *chatPage) View() string {
	width := p.width
	if width <= 0 {
		// Bubble Tea can render one frame before the first WindowSizeMsg. Keep
		// the identity/context line useful in that frame instead of dropping it.
		width = 80
	}
	header := workspaceHeader(width, p.app, p.session)
	if notice := p.renderRunNotice(width); notice != "" {
		header = notice
	}
	layoutView := p.layout.View()
	if header != "" {
		layoutView = lipgloss.JoinVertical(lipgloss.Top, header, layoutView)
	}

	if p.showCompletionDialog {
		_, layoutHeight := p.layout.GetSize()
		editorWidth, editorHeight := p.editor.GetSize()

		p.completionDialog.SetWidth(editorWidth)
		overlay := p.completionDialog.View()

		layoutView = layout.PlaceOverlay(
			0,
			1+layoutHeight-editorHeight-lipgloss.Height(overlay),
			overlay,
			layoutView,
			false,
		)
	}

	return layoutView
}

func (p *chatPage) BindingKeys() []key.Binding {
	bindings := layout.KeyMapToSlice(keyMap)
	bindings = append(bindings, p.messages.BindingKeys()...)
	bindings = append(bindings, p.editor.BindingKeys()...)
	return bindings
}

func NewChatPage(app *app.App) tea.Model {
	cg := completions.NewFileAndFolderContextGroup()
	completionDialog := dialog.NewCompletionDialogCmp(cg)

	messagesContainer := layout.NewContainer(
		chat.NewMessagesCmp(app),
		layout.WithPadding(1, 1, 0, 1),
	)
	editorContainer := layout.NewContainer(
		chat.NewEditorCmp(app),
		layout.WithBorder(true, false, false, false),
	)
	return &chatPage{
		app:              app,
		editor:           editorContainer,
		messages:         messagesContainer,
		completionDialog: completionDialog,
		runNotices:       make(map[string]runNotice),
		layout: layout.NewSplitPane(
			layout.WithLeftPanel(messagesContainer),
			layout.WithBottomPanel(editorContainer),
		),
	}
}
