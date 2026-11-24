package page

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	appmodel "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/gitsafety"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/project"
	"github.com/opencode-ai/opencode/internal/research"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

var DashboardPage PageID = "dashboard"

const dashboardRefreshInterval = 2 * time.Second

type dashboardTickMsg struct {
	generation uint64
	id         uint64
}

type dashboardSnapshot struct {
	agents    []string
	tasks     []string
	taskItems []engineering.Task
	memory    []memory.Entry
	claims    []research.SearchResult
	sources   []research.Source
	sessions  []session.Session
	git       gitsafety.Status
	resources project.Resources
	refreshed time.Time
}

type dashboardLoadedMsg struct {
	generation uint64
	requestID  uint64
	snapshot   dashboardSnapshot
	err        error
}

type dashboardTaskTransitionedMsg struct {
	taskID string
	status engineering.Status
	err    error
}

type dashboardLoader func(context.Context, *appmodel.App) (dashboardSnapshot, error)
type dashboardTickFactory func(dashboardTickMsg) tea.Cmd

var dashboardTaskActions = struct {
	Retry  key.Binding
	Pause  key.Binding
	Resume key.Binding
	Up     key.Binding
	Down   key.Binding
}{
	Retry:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "retry task")),
	Pause:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause task")),
	Resume: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "resume task")),
	Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "select task")),
	Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "select task")),
}

type dashboardPage struct {
	app       *appmodel.App
	width     int
	height    int
	active    int
	refresh   key.Binding
	views     []string
	agents    []string
	tasks     []string
	taskItems []engineering.Task
	selected  int
	memory    []memory.Entry
	claims    []research.SearchResult
	sources   []research.Source
	sessions  []session.Session
	git       gitsafety.Status
	resources project.Resources
	refreshed time.Time

	visible         bool
	generation      uint64
	nextTickID      uint64
	scheduledTickID uint64
	nextRequestID   uint64
	refreshInFlight bool
	refreshPending  bool
	refreshCtx      context.Context
	refreshCancel   context.CancelFunc
	loadSnapshot    dashboardLoader
	newTick         dashboardTickFactory
}

func NewDashboardPage(app *appmodel.App) tea.Model {
	return &dashboardPage{
		app:          app,
		views:        []string{"agents", "tasks", "memory", "research", "sources", "sessions", "workspace"},
		refresh:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh dashboard")),
		loadSnapshot: loadDashboardSnapshot,
		newTick:      newDashboardTick,
	}
}

func (p *dashboardPage) Init() tea.Cmd { return p.Activate() }

func (p *dashboardPage) Activate() tea.Cmd {
	if p.visible {
		return nil
	}
	p.visible = true
	p.generation++
	p.scheduledTickID = 0
	p.refreshInFlight = false
	p.refreshPending = false
	ctx, cancel := context.WithCancel(context.Background())
	p.refreshCtx = ctx
	p.refreshCancel = cancel
	return tea.Batch(p.requestRefresh(ctx), p.scheduleTick())
}

func (p *dashboardPage) Deactivate() {
	if !p.visible {
		return
	}
	p.visible = false
	p.generation++
	p.scheduledTickID = 0
	p.refreshInFlight = false
	p.refreshPending = false
	if p.refreshCancel != nil {
		p.refreshCancel()
	}
	p.refreshCtx = nil
	p.refreshCancel = nil
}

func newDashboardTick(msg dashboardTickMsg) tea.Cmd {
	return tea.Tick(dashboardRefreshInterval, func(time.Time) tea.Msg { return msg })
}

func (p *dashboardPage) scheduleTick() tea.Cmd {
	if !p.visible || p.scheduledTickID != 0 {
		return nil
	}
	p.nextTickID++
	p.scheduledTickID = p.nextTickID
	msg := dashboardTickMsg{generation: p.generation, id: p.scheduledTickID}
	if p.newTick == nil {
		p.newTick = newDashboardTick
	}
	return p.newTick(msg)
}

func (p *dashboardPage) requestRefresh(ctx context.Context) tea.Cmd {
	if !p.visible {
		return nil
	}
	if p.refreshInFlight {
		p.refreshPending = true
		return nil
	}
	p.refreshInFlight = true
	p.nextRequestID++
	generation, requestID := p.generation, p.nextRequestID
	loader := p.loadSnapshot
	if loader == nil {
		loader = loadDashboardSnapshot
	}
	application := p.app
	return func() tea.Msg {
		snapshot, err := loader(ctx, application)
		return dashboardLoadedMsg{generation: generation, requestID: requestID, snapshot: snapshot, err: err}
	}
}

func (p *dashboardPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = max(0, typed.Width), max(0, typed.Height)
	case dashboardTickMsg:
		if !p.visible || typed.generation != p.generation || typed.id != p.scheduledTickID {
			return p, nil
		}
		p.scheduledTickID = 0
		return p, tea.Batch(p.requestRefresh(p.refreshContext()), p.scheduleTick())
	case dashboardLoadedMsg:
		if !p.visible || typed.generation != p.generation || typed.requestID != p.nextRequestID {
			return p, nil
		}
		p.refreshInFlight = false
		if typed.err == nil {
			p.applySnapshot(typed.snapshot)
		}
		if p.refreshPending {
			p.refreshPending = false
			return p, p.requestRefresh(p.refreshContext())
		}
		return p, nil
	case dashboardTaskTransitionedMsg:
		if typed.err != nil {
			return p, util.ReportError(typed.err)
		}
		return p, tea.Batch(
			util.ReportInfo(fmt.Sprintf("task %s [%s]", typed.taskID, typed.status)),
			p.requestRefresh(p.refreshContext()),
		)
	case tea.KeyMsg:
		if key.Matches(typed, p.refresh) {
			return p, p.requestRefresh(p.refreshContext())
		}
		if p.active == 1 {
			switch {
			case key.Matches(typed, dashboardTaskActions.Up):
				p.selectTask(-1)
				return p, nil
			case key.Matches(typed, dashboardTaskActions.Down):
				p.selectTask(1)
				return p, nil
			case key.Matches(typed, dashboardTaskActions.Retry):
				return p, p.transitionSelectedTask(engineering.StatusRetrying)
			case key.Matches(typed, dashboardTaskActions.Pause):
				return p, p.transitionSelectedTask(engineering.StatusPaused)
			case key.Matches(typed, dashboardTaskActions.Resume):
				return p, p.transitionSelectedTask(engineering.StatusReady)
			}
		}
		if typed.String() >= "1" && typed.String() <= "7" {
			p.active = int(typed.String()[0] - '1')
			return p, p.requestRefresh(p.refreshContext())
		}
	}
	return p, nil
}

func (p *dashboardPage) refreshContext() context.Context {
	if p.refreshCtx == nil {
		return context.Background()
	}
	return p.refreshCtx
}

func loadDashboardSnapshot(ctx context.Context, application *appmodel.App) (dashboardSnapshot, error) {
	snapshot := dashboardSnapshot{refreshed: time.Now().UTC()}
	if application == nil {
		return snapshot, nil
	}
	if err := ctx.Err(); err != nil {
		return dashboardSnapshot{}, err
	}
	if application.Agents != nil {
		for _, agent := range application.Agents.List() {
			snapshot.agents = append(snapshot.agents, fmt.Sprintf("%s  %s  task=%s",
				dashboardField(string(agent.Status)),
				dashboardField(agent.Role),
				dashboardField(agent.TaskID),
			))
		}
	}
	if application.Workflow != nil {
		for _, task := range application.Workflow.Tasks() {
			snapshot.taskItems = append(snapshot.taskItems, task)
			snapshot.tasks = append(snapshot.tasks, fmt.Sprintf("%s  %s  agent=%s",
				dashboardField(string(task.Status)),
				dashboardField(task.Title),
				dashboardField(task.Agent),
			))
		}
	}
	root := config.WorkingDirectory()
	if store, err := memory.New(root); err == nil {
		snapshot.memory, _ = store.List("")
	}
	if store, err := research.New(root); err == nil {
		claims, _ := store.Claims()
		for _, claim := range claims {
			snapshot.claims = append(snapshot.claims, research.SearchResult{Claim: claim, Score: claim.Confidence})
		}
		snapshot.sources, _ = store.Sources()
	}
	if application.Sessions != nil {
		snapshot.sessions, _ = application.Sessions.List(ctx)
	}
	if err := ctx.Err(); err != nil {
		return dashboardSnapshot{}, err
	}
	snapshot.git, _ = gitsafety.Inspect(ctx, root)
	if err := ctx.Err(); err != nil {
		return dashboardSnapshot{}, err
	}
	snapshot.resources, _ = project.Discover(root)
	return snapshot, ctx.Err()
}

func (p *dashboardPage) applySnapshot(snapshot dashboardSnapshot) {
	selectedTaskID := ""
	if p.selected >= 0 && p.selected < len(p.taskItems) {
		selectedTaskID = p.taskItems[p.selected].ID
	}
	p.agents = snapshot.agents
	p.tasks = snapshot.tasks
	p.taskItems = snapshot.taskItems
	p.memory = snapshot.memory
	p.claims = snapshot.claims
	p.sources = snapshot.sources
	p.sessions = snapshot.sessions
	p.git = snapshot.git
	p.resources = snapshot.resources
	p.refreshed = snapshot.refreshed
	// Refreshes may insert, remove, or reorder tasks. Keep actions attached to
	// the task the user selected rather than silently targeting its old row.
	if selectedTaskID != "" {
		for i, task := range p.taskItems {
			if task.ID == selectedTaskID {
				p.selected = i
				return
			}
		}
	}
	p.selected = max(0, min(p.selected, len(p.taskItems)-1))
}

func (p *dashboardPage) View() string {
	if p.width <= 0 || p.height <= 0 {
		return ""
	}
	t := theme.CurrentTheme()
	base := styles.BaseStyle()
	header := base.Foreground(t.Primary()).Bold(true).Render("RSCH  ·  Operational dashboard")
	nav := make([]string, len(p.views))
	for i, view := range p.views {
		label := fmt.Sprintf("%d %s", i+1, dashboardField(view))
		style := base.Foreground(t.TextMuted())
		if i == p.active {
			style = style.Foreground(t.Primary()).Bold(true)
		}
		nav[i] = style.Render(label)
	}
	content := p.renderActive(base, t)
	footer := base.Foreground(t.TextMuted()).Render(p.footer())
	view := lipgloss.JoinVertical(lipgloss.Left, header, strings.Join(nav, "  "), "", content, "", footer)
	return base.Width(p.width).Height(p.height).Render(dashboardBoundedContent(view, p.width, p.height))
}

func (p *dashboardPage) renderActive(base lipgloss.Style, t theme.Theme) string {
	if p.active < 0 || p.active >= len(p.views) {
		p.active = 0
	}
	var lines []string
	switch p.views[p.active] {
	case "agents":
		lines = append(lines, "Agents", "──────")
		for _, agent := range p.agents {
			lines = append(lines, dashboardField(agent))
		}
	case "tasks":
		lines = append(lines, "Tasks", "─────")
		// Reserve the existing header, navigation, section title, and footer.
		// Window the rows so keyboard selection and its actions stay visible.
		visibleRows := max(1, p.height-7)
		start := max(0, p.selected-visibleRows+1)
		end := min(len(p.taskItems), start+visibleRows)
		for i := start; i < end; i++ {
			task := p.taskItems[i]
			marker := " "
			if i == p.selected {
				marker = ">"
			}
			lines = append(lines, fmt.Sprintf("%s %s  %s  agent=%s",
				marker,
				dashboardField(string(task.Status)),
				dashboardField(task.Title),
				dashboardField(task.Agent),
			))
		}
	case "memory":
		lines = append(lines, "Memory", "──────")
		for _, entry := range p.memory {
			lines = append(lines, fmt.Sprintf("%s  [%s/%s] %s",
				dashboardField(entry.ID),
				dashboardField(string(entry.Layer)),
				dashboardField(string(entry.Kind)),
				dashboardField(entry.Content),
			))
		}
	case "research":
		lines = append(lines, "Claims", "──────")
		for _, result := range p.claims {
			lines = append(lines, fmt.Sprintf("%s  [%s %.2f] %s",
				dashboardField(result.Claim.ID),
				dashboardField(result.Claim.Status),
				result.Score,
				dashboardField(result.Claim.Text),
			))
		}
	case "sources":
		lines = append(lines, "Sources", "───────")
		for _, source := range p.sources {
			lines = append(lines, fmt.Sprintf("%s  %s  %s",
				dashboardField(source.ID),
				dashboardField(source.URI),
				dashboardField(source.Location),
			))
		}
	case "sessions":
		lines = append(lines, "Sessions", "────────")
		for _, item := range p.sessions {
			lines = append(lines, fmt.Sprintf("%s  %s  messages=%d cost=%.4f",
				dashboardField(item.ID),
				dashboardField(item.Title),
				item.MessageCount,
				item.Cost,
			))
		}
	case "workspace":
		lines = append(lines, "Workspace", "─────────")
		if p.git.Available {
			lines = append(lines, fmt.Sprintf("Git: available  branch=%s  clean=%t", dashboardField(p.git.Branch), p.git.Clean))
		} else {
			lines = append(lines, "Git: unavailable  "+dashboardField(p.git.Reason))
		}
		lines = append(lines, fmt.Sprintf("Agents=%d  skills=%d  hooks=%d  plugins=%d", len(p.resources.Agents), len(p.resources.Skills), len(p.resources.Hooks), len(p.resources.Plugins)))
	}
	if len(lines) <= 2 {
		lines = append(lines, base.Foreground(t.TextMuted()).Render("No records yet. Use the CLI or start a workflow to create observable state."))
	}
	return base.Foreground(t.Text()).Render(strings.Join(lines, "\n"))
}

func (p *dashboardPage) GetSize() (int, int) { return p.width, p.height }
func (p *dashboardPage) SetSize(width, height int) tea.Cmd {
	p.width, p.height = max(0, width), max(0, height)
	return nil
}
func (p *dashboardPage) BindingKeys() []key.Binding {
	return []key.Binding{p.refresh, dashboardTaskActions.Up, dashboardTaskActions.Down, dashboardTaskActions.Retry, dashboardTaskActions.Pause, dashboardTaskActions.Resume}
}

func (p *dashboardPage) selectTask(delta int) {
	if len(p.taskItems) == 0 {
		return
	}
	p.selected = (p.selected + delta + len(p.taskItems)) % len(p.taskItems)
}

func (p *dashboardPage) transitionSelectedTask(next engineering.Status) tea.Cmd {
	if p.app == nil || p.app.Workflow == nil || len(p.taskItems) == 0 {
		return util.ReportWarn("No workflow task is available")
	}
	task := p.taskItems[p.selected]
	allowed := false
	switch next {
	case engineering.StatusRetrying:
		allowed = task.Status == engineering.StatusRunning || task.Status == engineering.StatusFailed
	case engineering.StatusPaused:
		allowed = task.Status == engineering.StatusRunning
	case engineering.StatusReady:
		allowed = task.Status == engineering.StatusPaused
	}
	if !allowed {
		return util.ReportWarn(fmt.Sprintf("Cannot transition task %s from %s to %s", task.ID, task.Status, next))
	}
	application := p.app
	return func() tea.Msg {
		_, err := application.TransitionTask(context.Background(), task.ID, next)
		return dashboardTaskTransitionedMsg{taskID: task.ID, status: next, err: err}
	}
}

func (p *dashboardPage) footer() string {
	parts := []string{"1-7 switch view", "r refresh"}
	if p.active == 1 && len(p.taskItems) > 0 {
		parts = append(parts, "↑/↓ select")
		task := p.taskItems[p.selected]
		if task.Status == engineering.StatusRunning || task.Status == engineering.StatusFailed {
			parts = append(parts, "y retry")
		}
		if task.Status == engineering.StatusRunning {
			parts = append(parts, "p pause")
		}
		if task.Status == engineering.StatusPaused {
			parts = append(parts, "u resume")
		}
	}
	return fmt.Sprintf("%s · updated %s", strings.Join(parts, " · "), p.refreshed.Format("15:04:05"))
}

// dashboardField makes persisted and provider-derived values safe for a
// single dashboard row. Terminal controls are removed before any width
// measurement or styling, and embedded whitespace cannot inject extra rows.
func dashboardField(value string) string {
	return strings.Join(strings.Fields(styles.SanitizeTerminalText(value)), " ")
}

func dashboardBoundedContent(value string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return strings.Join(lines, "\n")
}

var _ layout.Sizeable = (*dashboardPage)(nil)
var _ Lifecycle = (*dashboardPage)(nil)
