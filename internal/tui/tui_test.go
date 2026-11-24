package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/page"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type inertModel struct{}

func TestArgumentsDialogUsesCurrentTerminalSizeOnOpen(t *testing.T) {
	model := appModel{width: 24, height: 8}
	updated, _ := model.Update(dialog.ShowMultiArgumentsDialogMsg{
		CommandID: "example", Content: "$FIRST $SECOND", ArgNames: []string{"FIRST", "SECOND"},
	})
	model = updated.(appModel)
	view := model.multiArgumentsDialog.View()
	if lipgloss.Width(view) > model.width || lipgloss.Height(view) > model.height {
		t.Fatalf("new dialog exceeds terminal before resize: got %dx%d, terminal %dx%d", lipgloss.Width(view), lipgloss.Height(view), model.width, model.height)
	}
}

func (inertModel) Init() tea.Cmd                         { return nil }
func (m inertModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (inertModel) View() string                          { return "" }

type recordingLifecycleModel struct {
	initCount       int
	activateCount   int
	deactivateCount int
}

func (m *recordingLifecycleModel) Init() tea.Cmd {
	m.initCount++
	return nil
}

func (m *recordingLifecycleModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (*recordingLifecycleModel) View() string                          { return "" }
func (m *recordingLifecycleModel) Activate() tea.Cmd {
	m.activateCount++
	return nil
}
func (m *recordingLifecycleModel) Deactivate() { m.deactivateCount++ }

type compactErrorAgent struct {
	err       error
	sessionID string
}

func (*compactErrorAgent) Subscribe(context.Context) <-chan pubsub.Event[agent.AgentEvent] {
	return nil
}
func (*compactErrorAgent) Model() models.Model { return models.Model{} }
func (*compactErrorAgent) Run(context.Context, string, string, ...message.Attachment) (<-chan agent.AgentEvent, error) {
	return nil, nil
}
func (*compactErrorAgent) Cancel(string)             {}
func (*compactErrorAgent) IsSessionBusy(string) bool { return false }
func (*compactErrorAgent) IsBusy() bool              { return false }
func (*compactErrorAgent) Update(config.AgentName, models.ModelID) (models.Model, error) {
	return models.Model{}, nil
}
func (a *compactErrorAgent) Summarize(_ context.Context, sessionID string) error {
	a.sessionID = sessionID
	return a.err
}

type recordingPermissionDialog struct {
	requests []permission.PermissionRequest
}

func (p *recordingPermissionDialog) Init() tea.Cmd { return nil }
func (p *recordingPermissionDialog) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return p, nil
}
func (p *recordingPermissionDialog) View() string { return "permission" }
func (p *recordingPermissionDialog) BindingKeys() []key.Binding {
	return nil
}
func (p *recordingPermissionDialog) SetPermissions(request permission.PermissionRequest) tea.Cmd {
	p.requests = append(p.requests, request)
	return nil
}

type recordingPermissionService struct {
	granted        []string
	grantedSession []string
	denied         []string
}

func (s *recordingPermissionService) Subscribe(context.Context) <-chan pubsub.Event[permission.PermissionRequest] {
	return nil
}
func (s *recordingPermissionService) Grant(request permission.PermissionRequest) {
	s.granted = append(s.granted, request.ID)
}
func (s *recordingPermissionService) GrantPersistant(request permission.PermissionRequest) {
	s.grantedSession = append(s.grantedSession, request.ID)
}
func (s *recordingPermissionService) Deny(request permission.PermissionRequest) {
	s.denied = append(s.denied, request.ID)
}
func (*recordingPermissionService) Request(permission.CreatePermissionRequest) bool { return false }
func (*recordingPermissionService) RequestContext(context.Context, permission.CreatePermissionRequest) bool {
	return false
}
func (*recordingPermissionService) AutoApproveSession(string) {}

func TestSessionShortcutDoesNotClaimCtrlS(t *testing.T) {
	for _, shortcut := range keys.SwitchSession.Keys() {
		if shortcut == "ctrl+s" {
			t.Fatal("session switch still claims ctrl+s")
		}
	}
	want := "alt+s"
	found := false
	for _, shortcut := range keys.SwitchSession.Keys() {
		if shortcut == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("session switch does not expose %q", want)
	}
}

func TestActivateModalIsExclusive(t *testing.T) {
	model := appModel{
		showPermissions:          true,
		showFilepicker:           true,
		showHelp:                 true,
		showQuit:                 true,
		showSessionDialog:        true,
		showCommandDialog:        true,
		showModelDialog:          true,
		showInitDialog:           true,
		showThemeDialog:          true,
		showMultiArgumentsDialog: true,
	}

	model.activateModal(modalCommand)
	if got := model.activeModal(); got != modalCommand {
		t.Fatalf("active modal = %v, want command", got)
	}
	visible := []bool{
		model.showPermissions,
		model.showFilepicker,
		model.showHelp,
		model.showQuit,
		model.showSessionDialog,
		model.showCommandDialog,
		model.showModelDialog,
		model.showInitDialog,
		model.showThemeDialog,
		model.showMultiArgumentsDialog,
	}
	count := 0
	for _, show := range visible {
		if show {
			count++
		}
	}
	if count != 1 || !model.showCommandDialog {
		t.Fatalf("activateModal left %d dialogs visible", count)
	}
}

func TestCompactSynchronousErrorClearsOverlay(t *testing.T) {
	wantErr := errors.New("summarizer unavailable")
	coder := &compactErrorAgent{err: wantErr}
	model := appModel{
		app:             &app.App{CoderAgent: coder},
		selectedSession: session.Session{ID: "session-1"},
	}

	updated, cmd := model.Update(startCompactSessionMsg{})
	model = updated.(appModel)
	if !model.isCompacting {
		t.Fatal("compaction overlay did not open")
	}
	if cmd == nil {
		t.Fatal("compaction did not return a command")
	}

	result, ok := cmd().(compactSessionResultMsg)
	if !ok {
		t.Fatal("compaction command did not report its synchronous result")
	}
	if coder.sessionID != "session-1" || !errors.Is(result.err, wantErr) {
		t.Fatalf("summarize result = session %q, error %v", coder.sessionID, result.err)
	}

	updated, report := model.Update(result)
	model = updated.(appModel)
	if model.isCompacting || model.compactingMessage != "" {
		t.Fatalf("failed compaction left overlay active: active=%t message=%q", model.isCompacting, model.compactingMessage)
	}
	if report == nil {
		t.Fatal("failed compaction did not report an error")
	}
	info, ok := report().(util.InfoMsg)
	if !ok || info.Type != util.InfoTypeError || info.Msg != "failed to summarize session: "+wantErr.Error() {
		t.Fatalf("error report = %#v", info)
	}
}

func TestPermissionRequestsQueueBehindActiveModal(t *testing.T) {
	permissionDialog := &recordingPermissionDialog{}
	permissionService := &recordingPermissionService{}
	model := appModel{
		app:         &app.App{Permissions: permissionService},
		currentPage: page.DashboardPage,
		permissions: permissionDialog,
	}
	p1 := permission.PermissionRequest{ID: "p1", ToolName: "write", Path: "/workspace/one"}
	p2 := permission.PermissionRequest{ID: "p2", ToolName: "bash", Path: "/workspace/two"}

	updated, _ := model.Update(pubsub.Event[permission.PermissionRequest]{Type: pubsub.CreatedEvent, Payload: p1})
	model = updated.(appModel)
	updated, _ = model.Update(pubsub.Event[permission.PermissionRequest]{Type: pubsub.CreatedEvent, Payload: p2})
	model = updated.(appModel)

	if got := model.activeModal(); got != modalPermission {
		t.Fatalf("active modal = %v, want permission", got)
	}
	if len(model.permissionQueue) != 2 || model.permissionQueue[0].ID != "p1" || model.permissionQueue[1].ID != "p2" {
		t.Fatalf("permission queue = %#v, want [p1 p2]", model.permissionQueue)
	}
	if len(permissionDialog.requests) != 1 || permissionDialog.requests[0].ID != "p1" {
		t.Fatalf("shown requests = %#v, want only p1", permissionDialog.requests)
	}

	// Re-delivery of the same event must not add another prompt.
	updated, _ = model.Update(pubsub.Event[permission.PermissionRequest]{Type: pubsub.CreatedEvent, Payload: p2})
	model = updated.(appModel)
	if len(model.permissionQueue) != 2 || len(permissionDialog.requests) != 1 {
		t.Fatalf("duplicate request changed queue=%#v shown=%#v", model.permissionQueue, permissionDialog.requests)
	}

	// Only the queue head owns the decision. A stale response cannot resolve a
	// later request or change which prompt is visible.
	updated, _ = model.Update(dialog.PermissionResponseMsg{Action: dialog.PermissionAllow, Permission: p2})
	model = updated.(appModel)
	if len(permissionService.granted) != 0 || len(permissionService.denied) != 0 || len(model.permissionQueue) != 2 {
		t.Fatalf("stale response changed service=%#v/%#v queue=%#v", permissionService.granted, permissionService.denied, model.permissionQueue)
	}

	model.activateModal(modalHelp)
	if got := model.activeModal(); got != modalPermission {
		t.Fatalf("permission modal was replaced by %v", got)
	}

	updated, _ = model.Update(dialog.PermissionResponseMsg{Action: dialog.PermissionDeny, Permission: p1})
	model = updated.(appModel)
	if len(permissionService.denied) != 1 || permissionService.denied[0] != "p1" {
		t.Fatalf("denied requests = %v, want [p1]", permissionService.denied)
	}
	if len(model.permissionQueue) != 1 || model.permissionQueue[0].ID != "p2" {
		t.Fatalf("permission queue after p1 denial = %#v, want [p2]", model.permissionQueue)
	}
	if len(permissionDialog.requests) != 2 || permissionDialog.requests[1].ID != "p2" {
		t.Fatalf("shown requests after p1 denial = %#v, want [p1 p2]", permissionDialog.requests)
	}
	if got := model.activeModal(); got != modalPermission {
		t.Fatalf("active modal after p1 denial = %v, want permission", got)
	}

	updated, _ = model.Update(dialog.PermissionResponseMsg{Action: dialog.PermissionAllow, Permission: p2})
	model = updated.(appModel)
	if len(permissionService.granted) != 1 || permissionService.granted[0] != "p2" {
		t.Fatalf("granted requests = %v, want [p2]", permissionService.granted)
	}
	if len(model.permissionQueue) != 0 {
		t.Fatalf("permission queue after final response = %#v, want empty", model.permissionQueue)
	}
	if got := model.activeModal(); got != modalNone {
		t.Fatalf("active modal after final response = %v, want none", got)
	}
}

func TestActiveModalOwnsGlobalShortcut(t *testing.T) {
	model := appModel{
		showCommandDialog: true,
		commandDialog:     dialog.NewCommandDialogCmp(),
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := updated.(appModel)
	if got.activeModal() != modalCommand || got.showFilepicker {
		t.Fatalf("global filepicker shortcut escaped active command modal: active=%v filepicker=%t", got.activeModal(), got.showFilepicker)
	}
}

func TestDashboardEscapeMovesBack(t *testing.T) {
	model := appModel{
		currentPage:  page.DashboardPage,
		previousPage: page.ChatPage,
		pages: map[page.PageID]tea.Model{
			page.ChatPage:      inertModel{},
			page.DashboardPage: inertModel{},
		},
		loadedPages: map[page.PageID]bool{page.ChatPage: true, page.DashboardPage: true},
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(appModel)
	if got.currentPage != page.ChatPage {
		t.Fatalf("escape from dashboard moved to %q, want chat", got.currentPage)
	}
}

func TestMoveToPageTransfersLifecycleBetweenLoadedPages(t *testing.T) {
	dashboard := &recordingLifecycleModel{}
	workflow := &recordingLifecycleModel{}
	model := appModel{
		currentPage: page.DashboardPage,
		pages: map[page.PageID]tea.Model{
			page.DashboardPage: dashboard,
			page.WorkflowPage:  workflow,
		},
		loadedPages: map[page.PageID]bool{
			page.DashboardPage: true,
			page.WorkflowPage:  true,
		},
	}

	model.moveToPage(page.WorkflowPage)
	if dashboard.deactivateCount != 1 || workflow.activateCount != 1 {
		t.Fatalf("dashboard deactivations = %d, workflow activations = %d; want 1, 1", dashboard.deactivateCount, workflow.activateCount)
	}
	if dashboard.initCount != 0 || workflow.initCount != 0 {
		t.Fatalf("loaded page was initialized again: dashboard=%d workflow=%d", dashboard.initCount, workflow.initCount)
	}

	model.moveToPage(page.DashboardPage)
	if workflow.deactivateCount != 1 || dashboard.activateCount != 1 {
		t.Fatalf("workflow deactivations = %d, dashboard activations = %d; want 1, 1", workflow.deactivateCount, dashboard.activateCount)
	}
}

func TestHelpKeysAreReliableOutsideComposer(t *testing.T) {
	foundF1 := false
	for _, shortcut := range keys.Help.Keys() {
		foundF1 = foundF1 || shortcut == "f1"
	}
	if !foundF1 {
		t.Fatalf("help keys missing reliable bindings: %v", keys.Help.Keys())
	}

	model := appModel{currentPage: page.DashboardPage}
	if !model.bareHelpEnabled() {
		t.Fatal("bare help should be enabled outside the chat composer")
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !updated.(appModel).showHelp {
		t.Fatal("question mark did not open help on dashboard")
	}
}

func TestBareQuestionMarkNeverOwnsChatComposerInput(t *testing.T) {
	model := appModel{currentPage: page.ChatPage}
	if model.bareHelpEnabled() {
		t.Fatal("bare question mark must remain available to the chat composer")
	}
}

func TestShowHelpDialogMessageOpensPersistentOverlay(t *testing.T) {
	model := appModel{currentPage: page.ChatPage}

	updated, cmd := model.Update(dialog.ShowHelpDialogMsg{})
	got := updated.(appModel)
	if got.activeModal() != modalHelp || !got.showHelp {
		t.Fatalf("active modal = %v, showHelp = %t; want help overlay", got.activeModal(), got.showHelp)
	}
	if cmd != nil {
		t.Fatal("opening help overlay unexpectedly returned a command")
	}
}

func TestGlobalShortcutsPreserveReadlineEditingKeys(t *testing.T) {
	reserved := map[string]bool{
		"ctrl+a": true, "ctrl+d": true, "ctrl+e": true, "ctrl+f": true,
		"ctrl+h": true, "ctrl+k": true, "ctrl+n": true, "ctrl+t": true,
		"ctrl+u": true, "ctrl+w": true,
	}
	for _, binding := range layout.KeyMapToSlice(keys) {
		for _, shortcut := range binding.Keys() {
			if reserved[shortcut] {
				t.Fatalf("global shortcut %q steals a standard composer editing key", shortcut)
			}
		}
	}
}

func TestGlobalShortcutsPreserveTextareaEditingKeys(t *testing.T) {
	reserved := map[string]bool{
		// Readline reserves Meta-T for transpose-word even though the Bubbles
		// textarea does not currently expose it in DefaultKeyMap.
		"alt+t": true,
	}
	for _, binding := range layout.KeyMapToSlice(textarea.DefaultKeyMap) {
		for _, shortcut := range binding.Keys() {
			reserved[shortcut] = true
		}
	}

	for _, binding := range layout.KeyMapToSlice(keys) {
		for _, shortcut := range binding.Keys() {
			if reserved[shortcut] {
				t.Fatalf("global shortcut %q steals a standard textarea editing key", shortcut)
			}
		}
	}
}

func TestNavigationActionsUseFunctionKeysAndRemainDiscoverable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		binding   key.Binding
		wantKey   string
		commandID string
	}{
		{name: "image picker", binding: keys.Filepicker, wantKey: "f5", commandID: "attach"},
		{name: "logs", binding: keys.Logs, wantKey: "f6", commandID: "logs"},
		{name: "theme", binding: keys.SwitchTheme, wantKey: "f8", commandID: "theme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.binding.Keys(); len(got) != 1 || got[0] != tc.wantKey {
				t.Fatalf("keys = %v, want [%s]", got, tc.wantKey)
			}
		})
	}

	model := &appModel{}
	model.registerNavigationCommands()
	for _, commandID := range []string{"attach", "logs", "theme"} {
		command, ok := model.findCommand(commandID)
		if !ok || command.Handler == nil {
			t.Fatalf("navigation action %q is not discoverable in the palette", commandID)
		}
	}
}
