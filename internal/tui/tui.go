package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/sandbox"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/components/core"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/page"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type keyMap struct {
	Logs          key.Binding
	Quit          key.Binding
	Help          key.Binding
	SwitchSession key.Binding
	Commands      key.Binding
	Filepicker    key.Binding
	Models        key.Binding
	SwitchTheme   key.Binding
	Workflow      key.Binding
}

type startCompactSessionMsg struct{}

type showFilepickerMsg struct{}

type showThemeDialogMsg struct{}

type showSessionDialogMsg struct{}

type showModelDialogMsg struct{}

type sessionListLoadedMsg struct {
	sessions []session.Session
	err      error
}

type compactSessionResultMsg struct {
	err error
}

type modalID uint8

const (
	modalNone modalID = iota
	modalPermission
	modalFilepicker
	modalHelp
	modalQuit
	modalSession
	modalModel
	modalCommand
	modalInit
	modalTheme
	modalMultiArguments
)

const (
	quitKey = "q"
)

var keys = keyMap{
	Logs: key.NewBinding(
		key.WithKeys("f6"),
		key.WithHelp("f6", "logs"),
	),

	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
	Help: key.NewBinding(
		key.WithKeys("f1"),
		key.WithHelp("f1", "toggle help"),
	),

	SwitchSession: key.NewBinding(
		key.WithKeys("alt+s"),
		key.WithHelp("alt+s", "switch session"),
	),

	Commands: key.NewBinding(
		key.WithKeys("alt+p"),
		key.WithHelp("alt+p", "commands"),
	),
	Filepicker: key.NewBinding(
		key.WithKeys("f5"),
		key.WithHelp("f5", "select files to upload"),
	),
	Models: key.NewBinding(
		key.WithKeys("alt+m"),
		key.WithHelp("alt+m", "model selection"),
	),

	SwitchTheme: key.NewBinding(
		key.WithKeys("f8"),
		key.WithHelp("f8", "switch theme"),
	),
	Workflow: key.NewBinding(
		key.WithKeys("alt+w"),
		key.WithHelp("alt+w", "dashboard"),
	),
}

var helpEsc = key.NewBinding(
	key.WithKeys("?"),
	key.WithHelp("?", "toggle help"),
)

var returnKey = key.NewBinding(
	key.WithKeys("esc"),
	key.WithHelp("esc", "close"),
)

var logsKeyReturnKey = key.NewBinding(
	key.WithKeys("esc", "backspace", quitKey),
	key.WithHelp("esc/q", "go back"),
)

type appModel struct {
	width, height   int
	currentPage     page.PageID
	previousPage    page.PageID
	pages           map[page.PageID]tea.Model
	loadedPages     map[page.PageID]bool
	status          core.StatusCmp
	app             *app.App
	selectedSession session.Session

	showPermissions bool
	permissions     dialog.PermissionDialogCmp
	permissionQueue []permission.PermissionRequest

	showHelp bool
	help     dialog.HelpCmp

	showQuit bool
	quit     dialog.QuitDialog

	showSessionDialog bool
	sessionDialog     dialog.SessionDialog

	showCommandDialog bool
	commandDialog     dialog.CommandDialog
	commands          []dialog.Command

	showModelDialog bool
	modelDialog     dialog.ModelDialog

	showInitDialog bool
	initDialog     dialog.InitDialogCmp

	showFilepicker bool
	filepicker     dialog.FilepickerCmp

	showThemeDialog bool
	themeDialog     dialog.ThemeDialog

	showMultiArgumentsDialog bool
	multiArgumentsDialog     dialog.MultiArgumentsDialogCmp

	isCompacting      bool
	compactingMessage string
}

// activeModal is the single source of truth for keyboard routing. The boolean
// fields are retained to keep each dialog's rendering and lifecycle local, but
// all code that opens a modal goes through activateModal so focus cannot leak
// into a visually covered component.
func (a *appModel) activeModal() modalID {
	switch {
	case a.showMultiArgumentsDialog:
		return modalMultiArguments
	case a.showThemeDialog:
		return modalTheme
	case a.showInitDialog:
		return modalInit
	case a.showCommandDialog:
		return modalCommand
	case a.showModelDialog:
		return modalModel
	case a.showSessionDialog:
		return modalSession
	case a.showQuit:
		return modalQuit
	case a.showHelp:
		return modalHelp
	case a.showFilepicker:
		return modalFilepicker
	case a.showPermissions:
		return modalPermission
	default:
		return modalNone
	}
}

func (a *appModel) activateModal(modal modalID) {
	// Permission prompts are security-critical and remain the focus owner until
	// every queued request has received an explicit response.
	if len(a.permissionQueue) > 0 && modal != modalPermission {
		return
	}
	a.showPermissions = false
	a.showFilepicker = false
	a.showHelp = false
	a.showQuit = false
	a.showSessionDialog = false
	a.showModelDialog = false
	a.showCommandDialog = false
	a.showInitDialog = false
	a.showThemeDialog = false
	a.showMultiArgumentsDialog = false
	if a.filepicker != nil {
		a.filepicker.ToggleFilepicker(false)
	}

	switch modal {
	case modalPermission:
		a.showPermissions = true
	case modalFilepicker:
		a.showFilepicker = true
		if a.filepicker != nil {
			a.filepicker.ToggleFilepicker(true)
		}
	case modalHelp:
		a.showHelp = true
	case modalQuit:
		a.quit.Reset()
		a.showQuit = true
	case modalSession:
		a.showSessionDialog = true
	case modalModel:
		a.showModelDialog = true
	case modalCommand:
		a.showCommandDialog = true
	case modalInit:
		a.showInitDialog = true
	case modalTheme:
		a.showThemeDialog = true
	case modalMultiArguments:
		a.showMultiArgumentsDialog = true
	}
}

func (a *appModel) enqueuePermission(request permission.PermissionRequest) tea.Cmd {
	for _, queued := range a.permissionQueue {
		if queued.ID == request.ID {
			return nil
		}
	}

	a.permissionQueue = append(a.permissionQueue, request)
	if len(a.permissionQueue) > 1 {
		return nil
	}

	a.activateModal(modalPermission)
	return a.permissions.SetPermissions(request)
}

func (a *appModel) resolvePermission(response dialog.PermissionResponseMsg) tea.Cmd {
	if len(a.permissionQueue) == 0 || a.permissionQueue[0].ID != response.Permission.ID {
		return util.ReportWarn("Ignored a stale permission response")
	}
	if a.app == nil || a.app.Permissions == nil {
		return util.ReportError(fmt.Errorf("permission service is unavailable"))
	}

	request := a.permissionQueue[0]
	switch response.Action {
	case dialog.PermissionAllow:
		a.app.Permissions.Grant(request)
	case dialog.PermissionAllowForSession:
		a.app.Permissions.GrantPersistant(request)
	case dialog.PermissionDeny:
		a.app.Permissions.Deny(request)
	default:
		return util.ReportWarn("Ignored an unknown permission response")
	}

	a.permissionQueue = a.permissionQueue[1:]
	if len(a.permissionQueue) == 0 {
		a.activateModal(modalNone)
		return nil
	}

	a.activateModal(modalPermission)
	return a.permissions.SetPermissions(a.permissionQueue[0])
}

// Bare question marks must remain typeable in the chat composer, including
// while a run is active and the user is drafting a follow-up. F1 remains the
// reliable help key everywhere; ? is only a convenience on non-editor pages.
func (a *appModel) bareHelpEnabled() bool {
	return a.currentPage != page.ChatPage
}

func (a appModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmd := a.pages[a.currentPage].Init()
	a.loadedPages[a.currentPage] = true
	cmds = append(cmds, cmd)
	cmd = a.status.Init()
	cmds = append(cmds, cmd)
	cmd = a.quit.Init()
	cmds = append(cmds, cmd)
	cmd = a.help.Init()
	cmds = append(cmds, cmd)
	cmd = a.sessionDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.commandDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.modelDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.initDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.filepicker.Init()
	cmds = append(cmds, cmd)
	cmd = a.themeDialog.Init()
	cmds = append(cmds, cmd)

	// Check if we should show the init dialog
	cmds = append(cmds, func() tea.Msg {
		shouldShow, err := config.ShouldShowInitDialog()
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  "Failed to check init status: " + err.Error(),
			}
		}
		return dialog.ShowInitDialogMsg{Show: shouldShow}
	})

	return tea.Batch(cmds...)
}

func (a appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		msg.Height = max(0, msg.Height-1) // Make space for the status bar
		a.width, a.height = msg.Width, msg.Height

		s, _ := a.status.Update(msg)
		a.status = s.(core.StatusCmp)
		a.pages[a.currentPage], cmd = a.pages[a.currentPage].Update(msg)
		cmds = append(cmds, cmd)

		prm, permCmd := a.permissions.Update(msg)
		a.permissions = prm.(dialog.PermissionDialogCmp)
		cmds = append(cmds, permCmd)

		help, helpCmd := a.help.Update(msg)
		a.help = help.(dialog.HelpCmp)
		cmds = append(cmds, helpCmd)

		session, sessionCmd := a.sessionDialog.Update(msg)
		a.sessionDialog = session.(dialog.SessionDialog)
		cmds = append(cmds, sessionCmd)

		command, commandCmd := a.commandDialog.Update(msg)
		a.commandDialog = command.(dialog.CommandDialog)
		cmds = append(cmds, commandCmd)

		filepicker, filepickerCmd := a.filepicker.Update(msg)
		a.filepicker = filepicker.(dialog.FilepickerCmp)
		cmds = append(cmds, filepickerCmd)

		modelDialog, modelCmd := a.modelDialog.Update(msg)
		a.modelDialog = modelDialog.(dialog.ModelDialog)
		cmds = append(cmds, modelCmd)

		themeDialog, themeCmd := a.themeDialog.Update(msg)
		a.themeDialog = themeDialog.(dialog.ThemeDialog)
		cmds = append(cmds, themeCmd)

		a.initDialog.SetSize(msg.Width, msg.Height)

		if a.showMultiArgumentsDialog {
			a.multiArgumentsDialog.SetSize(msg.Width, msg.Height)
			args, argsCmd := a.multiArgumentsDialog.Update(msg)
			a.multiArgumentsDialog = args.(dialog.MultiArgumentsDialogCmp)
			cmds = append(cmds, argsCmd)
		}

		return a, tea.Batch(cmds...)
	// Status
	case util.InfoMsg:
		s, cmd := a.status.Update(msg)
		a.status = s.(core.StatusCmp)
		cmds = append(cmds, cmd)
		return a, tea.Batch(cmds...)
	case pubsub.Event[logging.LogMessage]:
		if msg.Payload.Persist {
			switch msg.Payload.Level {
			case "error":
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeError,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})
				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)
			case "info":
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeInfo,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})
				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)

			case "warn":
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeWarn,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})

				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)
			default:
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeInfo,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})
				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)
			}
		}
	case events.Event:
		// The durable event stream is the workflow-level source for the TUI.
		// Keep normal progress quiet, but surface failures and policy/security
		// decisions immediately without exposing private model reasoning.
		switch msg.Type {
		case events.ToolFailed, events.TaskFailed, events.AgentFailed, events.SandboxDenied, events.SandboxFailed, events.SecurityFinding, events.GitConflict:
			return a, util.ReportWarn("workflow event: " + string(msg.Type))
		}
	case util.ClearStatusMsg:
		s, _ := a.status.Update(msg)
		a.status = s.(core.StatusCmp)

		// Permission
	case pubsub.Event[permission.PermissionRequest]:
		if msg.Type != pubsub.CreatedEvent {
			return a, nil
		}
		return a, a.enqueuePermission(msg.Payload)
	case dialog.PermissionResponseMsg:
		return a, a.resolvePermission(msg)

	case page.PageChangeMsg:
		return a, a.moveToPage(msg.ID)

	case dialog.CloseQuitMsg:
		a.showQuit = false
		return a, nil

	case dialog.CloseSessionDialogMsg:
		a.showSessionDialog = false
		return a, nil

	case dialog.CloseCommandDialogMsg:
		a.showCommandDialog = false
		return a, nil

	case dialog.ShowCommandDialogMsg:
		if a.currentPage == page.ChatPage && a.activeModal() == modalNone {
			a.commandDialog.SetCommands(a.commands)
			a.activateModal(modalCommand)
			return a, a.commandDialog.Init()
		}
		return a, nil

	case dialog.ShowHelpDialogMsg:
		if a.activeModal() == modalNone {
			a.activateModal(modalHelp)
		}
		return a, nil

	case showFilepickerMsg:
		if a.activeModal() == modalNone {
			a.activateModal(modalFilepicker)
		}
		return a, nil

	case showThemeDialogMsg:
		if a.activeModal() == modalNone {
			a.activateModal(modalTheme)
			return a, a.themeDialog.Init()
		}
		return a, nil

	case showSessionDialogMsg:
		if a.currentPage != page.ChatPage || a.activeModal() != modalNone || a.app == nil || a.app.Sessions == nil {
			return a, nil
		}
		service := a.app.Sessions
		return a, func() tea.Msg {
			sessions, err := service.List(context.Background())
			return sessionListLoadedMsg{sessions: sessions, err: err}
		}

	case sessionListLoadedMsg:
		if msg.err != nil {
			return a, util.ReportError(msg.err)
		}
		if len(msg.sessions) == 0 {
			return a, util.ReportWarn("No sessions available")
		}
		if a.currentPage == page.ChatPage && a.activeModal() == modalNone {
			a.sessionDialog.SetSessions(msg.sessions)
			a.activateModal(modalSession)
		}
		return a, nil

	case showModelDialogMsg:
		if a.currentPage == page.ChatPage && a.activeModal() == modalNone {
			a.activateModal(modalModel)
			return a, a.modelDialog.Init()
		}
		return a, nil

	case startCompactSessionMsg:
		// Start compacting the current session
		a.isCompacting = true
		a.compactingMessage = "Starting summarization..."

		if a.selectedSession.ID == "" {
			a.isCompacting = false
			return a, util.ReportWarn("No active session to summarize")
		}

		// Start the summarization process and always report its synchronous
		// result back through Update so the overlay cannot become stuck.
		coder := a.app.CoderAgent
		sessionID := a.selectedSession.ID
		return a, func() tea.Msg {
			ctx := context.Background()
			return compactSessionResultMsg{err: coder.Summarize(ctx, sessionID)}
		}

	case compactSessionResultMsg:
		if msg.err != nil {
			a.isCompacting = false
			a.compactingMessage = ""
			return a, util.ReportError(fmt.Errorf("failed to summarize session: %w", msg.err))
		}
		return a, nil

	case pubsub.Event[agent.AgentEvent]:
		payload := msg.Payload
		if payload.Error != nil {
			a.isCompacting = false
			return a, util.ReportError(payload.Error)
		}

		a.compactingMessage = payload.Progress

		if payload.Done && payload.Type == agent.AgentEventTypeSummarize {
			a.isCompacting = false
			return a, util.ReportInfo("Session summarization complete")
		} else if payload.Done && payload.Type == agent.AgentEventTypeResponse && a.selectedSession.ID != "" {
			model := a.app.CoderAgent.Model()
			contextWindow := model.ContextWindow
			tokens := a.selectedSession.CompletionTokens + a.selectedSession.PromptTokens
			if (tokens >= int64(float64(contextWindow)*0.95)) && config.Get().AutoCompact {
				return a, util.CmdHandler(startCompactSessionMsg{})
			}
		}
		// Continue listening for events
		return a, nil

	case dialog.CloseThemeDialogMsg:
		a.showThemeDialog = false
		return a, nil

	case dialog.ThemeChangedMsg:
		for id, pageModel := range a.pages {
			if !a.loadedPages[id] {
				continue
			}
			updated, pageCmd := pageModel.Update(msg)
			a.pages[id] = updated
			cmds = append(cmds, pageCmd)
		}
		a.showThemeDialog = false
		cmds = append(cmds, util.ReportInfo("Theme changed to: "+msg.ThemeName))
		return a, tea.Batch(cmds...)

	case dialog.CloseModelDialogMsg:
		a.showModelDialog = false
		return a, nil

	case dialog.ModelSelectedMsg:
		a.showModelDialog = false

		model, err := a.app.CoderAgent.Update(config.AgentCoder, msg.Model.ID)
		if err != nil {
			return a, util.ReportError(err)
		}

		return a, util.ReportInfo(fmt.Sprintf("Model changed to %s", model.Name))

	case dialog.ShowInitDialogMsg:
		if msg.Show {
			a.activateModal(modalInit)
		} else {
			a.showInitDialog = false
		}
		return a, nil

	case dialog.CloseInitDialogMsg:
		a.showInitDialog = false
		if msg.Initialize {
			// Run the initialization command
			for _, cmd := range a.commands {
				if cmd.ID == "init" {
					// Mark the project as initialized
					if err := config.MarkProjectInitialized(); err != nil {
						return a, util.ReportError(err)
					}
					return a, cmd.Handler(cmd)
				}
			}
		} else {
			// Mark the project as initialized without running the command
			if err := config.MarkProjectInitialized(); err != nil {
				return a, util.ReportError(err)
			}
		}
		return a, nil

	case chat.SessionSelectedMsg:
		a.selectedSession = msg
		a.sessionDialog.SetSelectedSession(msg.ID)
	case chat.SessionClearedMsg:
		a.selectedSession = session.Session{}
		a.sessionDialog.SetSelectedSession("")

	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == a.selectedSession.ID {
			a.selectedSession = msg.Payload
		}
	case dialog.SessionSelectedMsg:
		a.showSessionDialog = false
		if a.currentPage == page.ChatPage {
			return a, util.CmdHandler(chat.SessionSelectedMsg(msg.Session))
		}
		return a, nil

	case dialog.CommandSelectedMsg:
		a.showCommandDialog = false
		// Execute the command handler if available
		if msg.Command.Handler != nil {
			return a, msg.Command.Handler(msg.Command)
		}
		return a, util.ReportInfo("Command selected: " + msg.Command.Title)

	case dialog.ShowMultiArgumentsDialogMsg:
		// Show multi-arguments dialog
		a.multiArgumentsDialog = dialog.NewMultiArgumentsDialogCmp(msg.CommandID, msg.Content, msg.ArgNames)
		a.multiArgumentsDialog.SetSize(a.width, a.height)
		a.activateModal(modalMultiArguments)
		return a, a.multiArgumentsDialog.Init()

	case dialog.CloseMultiArgumentsDialogMsg:
		// Close multi-arguments dialog
		a.showMultiArgumentsDialog = false

		// If submitted, replace all named arguments and run the command
		if msg.Submit {
			// Expand the original template exactly once before handing it to chat.
			return a, util.CmdHandler(dialog.CommandRunCustomMsg{
				Content: dialog.ExpandCommandArguments(msg.Content, msg.Args),
			})
		}
		return a, nil

	case tea.KeyMsg:
		// A visible modal owns the keyboard. Routing it before global shortcuts
		// prevents stacked dialogs and keeps input focus aligned with what is on
		// top of the screen.
		switch a.activeModal() {
		case modalMultiArguments:
			args, modalCmd := a.multiArgumentsDialog.Update(msg)
			a.multiArgumentsDialog = args.(dialog.MultiArgumentsDialogCmp)
			return a, modalCmd
		case modalTheme:
			themeDialog, modalCmd := a.themeDialog.Update(msg)
			a.themeDialog = themeDialog.(dialog.ThemeDialog)
			return a, modalCmd
		case modalInit:
			initDialog, modalCmd := a.initDialog.Update(msg)
			a.initDialog = initDialog.(dialog.InitDialogCmp)
			return a, modalCmd
		case modalCommand:
			commandDialog, modalCmd := a.commandDialog.Update(msg)
			a.commandDialog = commandDialog.(dialog.CommandDialog)
			return a, modalCmd
		case modalModel:
			if key.Matches(msg, keys.Models) {
				a.activateModal(modalNone)
				return a, nil
			}
			modelDialog, modalCmd := a.modelDialog.Update(msg)
			a.modelDialog = modelDialog.(dialog.ModelDialog)
			return a, modalCmd
		case modalSession:
			sessionDialog, modalCmd := a.sessionDialog.Update(msg)
			a.sessionDialog = sessionDialog.(dialog.SessionDialog)
			return a, modalCmd
		case modalQuit:
			if key.Matches(msg, keys.Quit) || key.Matches(msg, returnKey) {
				a.activateModal(modalNone)
				return a, nil
			}
			quitDialog, modalCmd := a.quit.Update(msg)
			a.quit = quitDialog.(dialog.QuitDialog)
			return a, modalCmd
		case modalHelp:
			if key.Matches(msg, keys.Help) || key.Matches(msg, helpEsc) ||
				key.Matches(msg, returnKey) || msg.String() == quitKey {
				a.activateModal(modalNone)
			}
			return a, nil
		case modalFilepicker:
			if key.Matches(msg, keys.Filepicker) ||
				(key.Matches(msg, returnKey) && !a.filepicker.IsCWDFocused()) {
				a.activateModal(modalNone)
				return a, nil
			}
			filepicker, modalCmd := a.filepicker.Update(msg)
			a.filepicker = filepicker.(dialog.FilepickerCmp)
			return a, modalCmd
		case modalPermission:
			permissions, modalCmd := a.permissions.Update(msg)
			a.permissions = permissions.(dialog.PermissionDialogCmp)
			return a, modalCmd
		}

		switch {

		case key.Matches(msg, keys.Quit):
			a.activateModal(modalQuit)
			return a, nil
		case key.Matches(msg, keys.SwitchSession):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions && !a.showCommandDialog {
				return a, util.CmdHandler(showSessionDialogMsg{})
			}
			return a, nil
		case key.Matches(msg, keys.Commands):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions && !a.showSessionDialog && !a.showThemeDialog && !a.showFilepicker {
				// Show commands dialog
				if len(a.commands) == 0 {
					return a, util.ReportWarn("No commands available")
				}
				a.commandDialog.SetCommands(a.commands)
				a.activateModal(modalCommand)
				return a, a.commandDialog.Init()
			}
			return a, nil
		case key.Matches(msg, keys.Models):
			if a.showModelDialog {
				a.showModelDialog = false
				return a, nil
			}
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions && !a.showSessionDialog && !a.showCommandDialog {
				a.activateModal(modalModel)
				return a, a.modelDialog.Init()
			}
			return a, nil
		case key.Matches(msg, keys.SwitchTheme):
			if !a.showQuit && !a.showPermissions && !a.showSessionDialog && !a.showCommandDialog {
				// Show theme switcher dialog
				a.activateModal(modalTheme)
				// Theme list is dynamically loaded by the dialog component
				return a, a.themeDialog.Init()
			}
			return a, nil
		case key.Matches(msg, logsKeyReturnKey):
			if a.currentPage != page.ChatPage {
				return a, a.moveBack()
			}
		case key.Matches(msg, keys.Logs):
			return a, a.moveToPage(page.LogsPage)
		case key.Matches(msg, keys.Workflow):
			return a, a.moveToPage(page.DashboardPage)
		case key.Matches(msg, keys.Help):
			a.activateModal(modalHelp)
			return a, nil
		case key.Matches(msg, helpEsc):
			if a.bareHelpEnabled() {
				a.activateModal(modalHelp)
				return a, nil
			}
		case key.Matches(msg, keys.Filepicker):
			a.activateModal(modalFilepicker)
			return a, nil
		}
	}

	// Non-key messages (cursor blink, spinners, service events) continue to the
	// active modal and the page beneath it. Key messages have already returned
	// from the exclusive routing block above.
	switch a.activeModal() {
	case modalMultiArguments:
		d, dialogCmd := a.multiArgumentsDialog.Update(msg)
		a.multiArgumentsDialog = d.(dialog.MultiArgumentsDialogCmp)
		cmds = append(cmds, dialogCmd)
	case modalFilepicker:
		f, filepickerCmd := a.filepicker.Update(msg)
		a.filepicker = f.(dialog.FilepickerCmp)
		cmds = append(cmds, filepickerCmd)
	case modalQuit:
		q, quitCmd := a.quit.Update(msg)
		a.quit = q.(dialog.QuitDialog)
		cmds = append(cmds, quitCmd)
	case modalPermission:
		d, permissionsCmd := a.permissions.Update(msg)
		a.permissions = d.(dialog.PermissionDialogCmp)
		cmds = append(cmds, permissionsCmd)
	case modalSession:
		d, sessionCmd := a.sessionDialog.Update(msg)
		a.sessionDialog = d.(dialog.SessionDialog)
		cmds = append(cmds, sessionCmd)
	case modalCommand:
		d, commandCmd := a.commandDialog.Update(msg)
		a.commandDialog = d.(dialog.CommandDialog)
		cmds = append(cmds, commandCmd)
	case modalModel:
		d, modelCmd := a.modelDialog.Update(msg)
		a.modelDialog = d.(dialog.ModelDialog)
		cmds = append(cmds, modelCmd)
	case modalInit:
		d, initCmd := a.initDialog.Update(msg)
		a.initDialog = d.(dialog.InitDialogCmp)
		cmds = append(cmds, initCmd)
	case modalTheme:
		d, themeCmd := a.themeDialog.Update(msg)
		a.themeDialog = d.(dialog.ThemeDialog)
		cmds = append(cmds, themeCmd)
	}

	s, _ := a.status.Update(msg)
	a.status = s.(core.StatusCmp)
	if _, isKey := msg.(tea.KeyMsg); isKey {
		a.pages[a.currentPage], cmd = a.pages[a.currentPage].Update(msg)
		cmds = append(cmds, cmd)
	} else {
		// Service events and asynchronous command results belong to their
		// owning page even when the user is temporarily viewing another page.
		// Key presses remain exclusive to the visible page.
		for id, pageModel := range a.pages {
			if !a.loadedPages[id] {
				continue
			}
			updated, pageCmd := pageModel.Update(msg)
			a.pages[id] = updated
			cmds = append(cmds, pageCmd)
		}
	}
	return a, tea.Batch(cmds...)
}

// RegisterCommand adds a command to the command dialog
func (a *appModel) RegisterCommand(cmd dialog.Command) {
	a.commands = append(a.commands, cmd)
}

func (a *appModel) findCommand(id string) (dialog.Command, bool) {
	for _, cmd := range a.commands {
		if cmd.ID == id {
			return cmd, true
		}
	}
	return dialog.Command{}, false
}

func (a *appModel) registerNavigationCommands() {
	a.RegisterCommand(dialog.Command{
		ID:          "sessions",
		Title:       "Switch Session",
		Description: "Browse conversation history (Alt+S)",
		Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(showSessionDialogMsg{})
		},
	})
	a.RegisterCommand(dialog.Command{
		ID:          "models",
		Title:       "Switch Model",
		Description: "Choose the coding model (Alt+M)",
		Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(showModelDialogMsg{})
		},
	})
	a.RegisterCommand(dialog.Command{
		ID:          "dashboard",
		Title:       "Open Dashboard",
		Description: "Inspect tasks, agents, and workspace (Alt+W)",
		Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(page.PageChangeMsg{ID: page.DashboardPage})
		},
	})
	a.RegisterCommand(dialog.Command{
		ID:          "attach",
		Title:       "Attach Image",
		Description: "Open the image file picker (F5)",
		Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(showFilepickerMsg{})
		},
	})
	a.RegisterCommand(dialog.Command{
		ID:          "logs",
		Title:       "Open Logs",
		Description: "Inspect application logs (F6)",
		Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(page.PageChangeMsg{ID: page.LogsPage})
		},
	})
	a.RegisterCommand(dialog.Command{
		ID:          "theme",
		Title:       "Switch Theme",
		Description: "Choose the terminal theme (F8)",
		Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(showThemeDialogMsg{})
		},
	})
}

func (a *appModel) moveToPage(pageID page.PageID) tea.Cmd {
	if pageID == a.currentPage {
		return nil
	}
	if current, ok := a.pages[a.currentPage].(page.Lifecycle); ok {
		current.Deactivate()
	}

	var cmds []tea.Cmd
	if _, ok := a.loadedPages[pageID]; !ok {
		cmd := a.pages[pageID].Init()
		cmds = append(cmds, cmd)
		a.loadedPages[pageID] = true
	} else if next, ok := a.pages[pageID].(page.Lifecycle); ok {
		cmds = append(cmds, next.Activate())
	}
	a.previousPage = a.currentPage
	a.currentPage = pageID
	if sizable, ok := a.pages[a.currentPage].(layout.Sizeable); ok {
		cmd := sizable.SetSize(a.width, a.height)
		cmds = append(cmds, cmd)
	}

	return tea.Batch(cmds...)
}

func (a *appModel) moveBack() tea.Cmd {
	return a.moveToPage(page.ChatPage)
}

func (a appModel) View() string {
	components := []string{
		a.pages[a.currentPage].View(),
	}

	components = append(components, a.status.View())

	appView := lipgloss.JoinVertical(lipgloss.Top, components...)

	if a.showPermissions {
		overlay := a.permissions.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showFilepicker {
		overlay := a.filepicker.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)

	}

	// Show compacting status overlay
	if a.isCompacting {
		t := theme.CurrentTheme()
		style := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.BorderFocused()).
			BorderBackground(t.Background()).
			Padding(1, 2).
			Background(t.Background()).
			Foreground(t.Text())

		overlay := style.Render("Summarizing\n" + a.compactingMessage)
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showHelp {
		bindings := layout.KeyMapToSlice(keys)
		if p, ok := a.pages[a.currentPage].(layout.Bindings); ok {
			bindings = append(bindings, p.BindingKeys()...)
		}
		if a.showPermissions {
			bindings = append(bindings, a.permissions.BindingKeys()...)
		}
		if a.currentPage == page.LogsPage || a.currentPage == page.WorkflowPage || a.currentPage == page.DashboardPage {
			bindings = append(bindings, logsKeyReturnKey)
		}
		if a.bareHelpEnabled() {
			bindings = append(bindings, helpEsc)
		}
		a.help.SetBindings(bindings)

		overlay := a.help.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showQuit {
		overlay := a.quit.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showSessionDialog {
		overlay := a.sessionDialog.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showModelDialog {
		overlay := a.modelDialog.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showCommandDialog {
		overlay := a.commandDialog.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showInitDialog {
		overlay := a.initDialog.View()
		appView = layout.PlaceOverlay(
			a.width/2-lipgloss.Width(overlay)/2,
			a.height/2-lipgloss.Height(overlay)/2,
			overlay,
			appView,
			true,
		)
	}

	if a.showThemeDialog {
		overlay := a.themeDialog.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	if a.showMultiArgumentsDialog {
		overlay := a.multiArgumentsDialog.View()
		row := lipgloss.Height(appView) / 2
		row -= lipgloss.Height(overlay) / 2
		col := lipgloss.Width(appView) / 2
		col -= lipgloss.Width(overlay) / 2
		appView = layout.PlaceOverlay(
			col,
			row,
			overlay,
			appView,
			true,
		)
	}

	return appView
}

func New(app *app.App) tea.Model {
	startPage := page.ChatPage
	model := &appModel{
		currentPage:   startPage,
		loadedPages:   make(map[page.PageID]bool),
		status:        core.NewStatusCmpWithClientProvider(app.LSPClients),
		help:          dialog.NewHelpCmp(),
		quit:          dialog.NewQuitCmp(),
		sessionDialog: dialog.NewSessionDialogCmp(),
		commandDialog: dialog.NewCommandDialogCmp(),
		modelDialog:   dialog.NewModelDialogCmp(),
		permissions:   dialog.NewPermissionDialogCmp(),
		initDialog:    dialog.NewInitDialogCmp(),
		themeDialog:   dialog.NewThemeDialogCmp(),
		app:           app,
		commands:      []dialog.Command{},
		pages: map[page.PageID]tea.Model{
			page.ChatPage:      page.NewChatPage(app),
			page.LogsPage:      page.NewLogsPage(),
			page.WorkflowPage:  page.NewWorkflowPage(app),
			page.DashboardPage: page.NewDashboardPage(app),
		},
		filepicker: dialog.NewFilepickerCmp(app),
	}
	model.registerNavigationCommands()

	model.RegisterCommand(dialog.Command{
		ID:          "init",
		Title:       "Initialize Project",
		Description: "Create/Update the rsch.md memory file",
		Handler: func(cmd dialog.Command) tea.Cmd {
			prompt := `Please analyze this codebase and create an rsch.md file containing:
1. Build/lint/test commands - especially for running a single test
2. Code style guidelines including imports, formatting, types, naming conventions, error handling, etc.

The file you create will be given to agentic coding agents (such as yourself) that operate in this repository. Make it about 20 lines long.
If there's already an rsch.md, improve it.
If there are Cursor rules (in .cursor/rules/ or .cursorrules) or Copilot rules (in .github/copilot-instructions.md), make sure to include them.`
			return tea.Batch(
				util.CmdHandler(chat.SendMsg{
					Text: prompt,
				}),
			)
		},
	})

	model.RegisterCommand(dialog.Command{
		ID: "status", Title: "Workflow Status", Description: "Show persisted goals, tasks, and active agents",
		Handler: func(dialog.Command) tea.Cmd {
			return util.ReportInfo(fmt.Sprintf("goals: %d  tasks: %d  agents: %d", len(app.Workflow.Goals()), len(app.Workflow.Tasks()), len(app.Agents.List())))
		},
	})
	model.RegisterCommand(dialog.Command{
		ID: "tasks", Title: "Show Tasks", Description: "List persisted workflow tasks",
		Handler: func(dialog.Command) tea.Cmd {
			items := app.Workflow.Tasks()
			if len(items) == 0 {
				return util.ReportInfo("no persisted tasks")
			}
			lines := make([]string, 0, len(items))
			for _, item := range items {
				lines = append(lines, fmt.Sprintf("%s [%s] %s", item.ID, item.Status, item.Title))
			}
			return util.ReportInfo(strings.Join(lines, "\n"))
		},
	})
	model.RegisterCommand(dialog.Command{
		ID: "agents", Title: "Show Agents", Description: "List durable agent lifecycle state",
		Handler: func(dialog.Command) tea.Cmd {
			items := app.Agents.List()
			if len(items) == 0 {
				return util.ReportInfo("no persisted agents")
			}
			lines := make([]string, 0, len(items))
			for _, item := range items {
				lines = append(lines, fmt.Sprintf("%s [%s] %s task=%s", item.ID, item.Status, item.Role, item.TaskID))
			}
			return util.ReportInfo(strings.Join(lines, "\n"))
		},
	})
	model.RegisterCommand(dialog.Command{
		ID: "security", Title: "Security Capabilities", Description: "Show the detected platform sandbox backend",
		Handler: func(dialog.Command) tea.Cmd {
			capability := sandbox.Detect()
			state := "unavailable"
			if capability.Isolated {
				state = "isolated"
			}
			return util.ReportInfo(fmt.Sprintf("%s: %s (%s)", capability.Platform, state, capability.Backend))
		},
	})
	for _, workflowCommand := range []struct{ id, title string }{
		{"help", "Help"}, {"goal", "Goal"}, {"interview", "Interview"}, {"plan", "Plan"},
		{"spec", "Specification"}, {"run", "Run"}, {"context", "Context"}, {"memory", "Memory"},
		{"search", "Search"}, {"test", "Test"}, {"fix", "Fix"}, {"verify", "Verify"},
		{"review", "Review"}, {"performance", "Performance"}, {"explain", "Explain"},
		{"why", "Why"}, {"diff", "Diff"}, {"git", "Git"}, {"checkpoint", "Checkpoint"},
		{"rewind", "Rewind"}, {"fork", "Fork"}, {"experiment", "Experiment"}, {"cost", "Cost"},
	} {
		workflowCommand := workflowCommand
		model.RegisterCommand(dialog.Command{ID: workflowCommand.id, Title: workflowCommand.title, Description: "Run /" + workflowCommand.id, Handler: func(dialog.Command) tea.Cmd {
			return util.CmdHandler(chat.SendMsg{Text: "/" + workflowCommand.id})
		}})
	}

	model.RegisterCommand(dialog.Command{
		ID:          "compact",
		Title:       "Compact Session",
		Description: "Summarize the current session and create a new one with the summary",
		Handler: func(cmd dialog.Command) tea.Cmd {
			return func() tea.Msg {
				return startCompactSessionMsg{}
			}
		},
	})
	// Load custom commands
	customCommands, err := dialog.LoadCustomCommands()
	if err != nil {
		logging.Warn("Failed to load custom commands", "error", err)
	} else {
		for _, cmd := range customCommands {
			model.RegisterCommand(cmd)
		}
	}

	return model
}
