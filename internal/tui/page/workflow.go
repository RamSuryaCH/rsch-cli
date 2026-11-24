package page

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	appmodel "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

var WorkflowPage PageID = "workflow"

const workflowRefreshInterval = 2 * time.Second

type workflowTickMsg struct {
	generation uint64
	id         uint64
}

type workflowSnapshot struct {
	goals         []engineering.Goal
	tasks         []engineering.Task
	evidenceCount map[string]int
	agents        []orchestration.Agent
	sessions      []session.Session
	lastRefresh   time.Time
}

type workflowLoadedMsg struct {
	generation uint64
	requestID  uint64
	snapshot   workflowSnapshot
	err        error
}

type workflowLoader func(context.Context, *appmodel.App) (workflowSnapshot, error)
type workflowTickFactory func(workflowTickMsg) tea.Cmd

type workflowPage struct {
	app           *appmodel.App
	width, height int
	goals         []engineering.Goal
	tasks         []engineering.Task
	evidenceCount map[string]int
	agents        []orchestration.Agent
	sessions      []session.Session
	lastRefresh   time.Time
	refresh       key.Binding

	visible         bool
	generation      uint64
	nextTickID      uint64
	scheduledTickID uint64
	nextRequestID   uint64
	refreshInFlight bool
	refreshPending  bool
	refreshCtx      context.Context
	refreshCancel   context.CancelFunc
	loadSnapshot    workflowLoader
	newTick         workflowTickFactory
}

func NewWorkflowPage(app *appmodel.App) layoutPage {
	return &workflowPage{
		app:           app,
		evidenceCount: map[string]int{},
		refresh:       key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh workflow")),
		loadSnapshot:  loadWorkflowSnapshot,
		newTick:       newWorkflowTick,
	}
}

// layoutPage keeps the page package's public surface small while satisfying
// the TUI layout contracts used by the root model.
type layoutPage interface {
	tea.Model
	layout.Sizeable
	layout.Bindings
}

func (p *workflowPage) Init() tea.Cmd { return p.Activate() }

func (p *workflowPage) Activate() tea.Cmd {
	if p.visible {
		return nil
	}
	p.visible = true
	p.generation++
	p.scheduledTickID = 0
	p.refreshInFlight = false
	p.refreshPending = false
	p.refreshCtx, p.refreshCancel = context.WithCancel(context.Background())
	return tea.Batch(p.requestRefresh(), p.scheduleTick())
}

func (p *workflowPage) Deactivate() {
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

func newWorkflowTick(msg workflowTickMsg) tea.Cmd {
	return tea.Tick(workflowRefreshInterval, func(time.Time) tea.Msg { return msg })
}

func (p *workflowPage) scheduleTick() tea.Cmd {
	if !p.visible || p.scheduledTickID != 0 {
		return nil
	}
	p.nextTickID++
	p.scheduledTickID = p.nextTickID
	msg := workflowTickMsg{generation: p.generation, id: p.scheduledTickID}
	if p.newTick == nil {
		p.newTick = newWorkflowTick
	}
	return p.newTick(msg)
}

func (p *workflowPage) requestRefresh() tea.Cmd {
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
	ctx := p.refreshCtx
	if ctx == nil {
		ctx = context.Background()
	}
	loader := p.loadSnapshot
	if loader == nil {
		loader = loadWorkflowSnapshot
	}
	application := p.app
	return func() tea.Msg {
		snapshot, err := loader(ctx, application)
		return workflowLoadedMsg{generation: generation, requestID: requestID, snapshot: snapshot, err: err}
	}
}

func (p *workflowPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = max(0, typed.Width), max(0, typed.Height)
	case workflowTickMsg:
		if !p.visible || typed.generation != p.generation || typed.id != p.scheduledTickID {
			return p, nil
		}
		p.scheduledTickID = 0
		return p, tea.Batch(p.requestRefresh(), p.scheduleTick())
	case workflowLoadedMsg:
		if !p.visible || typed.generation != p.generation || typed.requestID != p.nextRequestID {
			return p, nil
		}
		p.refreshInFlight = false
		if typed.err == nil {
			p.applySnapshot(typed.snapshot)
		}
		if p.refreshPending {
			p.refreshPending = false
			return p, p.requestRefresh()
		}
		return p, nil
	case tea.KeyMsg:
		if key.Matches(typed, p.refresh) {
			return p, p.requestRefresh()
		}
	}
	return p, nil
}

func loadWorkflowSnapshot(ctx context.Context, application *appmodel.App) (workflowSnapshot, error) {
	snapshot := workflowSnapshot{
		evidenceCount: make(map[string]int),
		lastRefresh:   time.Now().UTC(),
	}
	if application == nil {
		return snapshot, nil
	}
	if err := ctx.Err(); err != nil {
		return workflowSnapshot{}, err
	}
	if application.Workflow != nil {
		snapshot.goals = application.Workflow.Goals()
		snapshot.tasks = application.Workflow.Tasks()
		snapshot.evidenceCount = make(map[string]int, len(snapshot.tasks))
		for _, task := range snapshot.tasks {
			if err := ctx.Err(); err != nil {
				return workflowSnapshot{}, err
			}
			if evidence, err := application.Workflow.LoadEvidence(ctx, task.ID); err == nil {
				snapshot.evidenceCount[task.ID] = len(evidence)
			}
		}
	}
	if application.Agents != nil {
		snapshot.agents = application.Agents.List()
	}
	if application.Sessions != nil {
		snapshot.sessions, _ = application.Sessions.List(ctx)
	}
	return snapshot, ctx.Err()
}

func (p *workflowPage) applySnapshot(snapshot workflowSnapshot) {
	p.goals = snapshot.goals
	p.tasks = snapshot.tasks
	p.evidenceCount = snapshot.evidenceCount
	p.agents = snapshot.agents
	p.sessions = snapshot.sessions
	p.lastRefresh = snapshot.lastRefresh
}

func (p *workflowPage) View() string {
	if p.width <= 0 || p.height <= 0 {
		return ""
	}
	lines := []string{
		"Workflow status",
		fmt.Sprintf("Goals: %d    Tasks: %d    Agents: %d", len(p.goals), len(p.tasks), len(p.agents)),
		fmt.Sprintf("Last refresh: %s", p.lastRefresh.Format(time.RFC3339)),
		"",
		"Goals:",
	}
	for _, goal := range p.goals {
		lines = append(lines, fmt.Sprintf("  [%s] %s (%s)",
			workflowField(goal.ID),
			workflowField(goal.Title),
			workflowField(string(goal.Status)),
		))
	}
	if len(p.goals) == 0 {
		lines = append(lines, "  none")
	}
	lines = append(lines, "", "Sessions:")
	if len(p.sessions) == 0 {
		lines = append(lines, "  none")
	} else {
		limit := len(p.sessions)
		if limit > 5 {
			limit = 5
		}
		for _, item := range p.sessions[:limit] {
			title := workflowField(item.Title)
			if title == "" {
				title = "untitled"
			}
			lines = append(lines, fmt.Sprintf("  [%s] %s messages=%d cost=%.6f", workflowField(item.ID), title, item.MessageCount, item.Cost))
		}
	}
	lines = append(lines, "", "Tasks:")
	for _, task := range p.tasks {
		command := ""
		if len(task.Command) > 0 {
			parts := make([]string, len(task.Command))
			for i, part := range task.Command {
				parts[i] = workflowField(part)
			}
			command = " command=" + strings.Join(parts, " ")
		}
		verification := ""
		if len(task.Verification) > 0 {
			verification = fmt.Sprintf(" verify=%d", len(task.Verification))
		}
		lines = append(lines, fmt.Sprintf("  [%s] %s (%s) evidence=%d%s%s",
			workflowField(task.ID),
			workflowField(task.Title),
			workflowField(string(task.Status)),
			p.evidenceCount[task.ID],
			command,
			verification,
		))
	}
	if len(p.tasks) == 0 {
		lines = append(lines, "  none")
	}
	lines = append(lines, "", "Agents:")
	for _, agent := range p.agents {
		lines = append(lines, fmt.Sprintf("  [%s] %s (%s)",
			workflowField(agent.ID),
			workflowField(agent.Role),
			workflowField(string(agent.Status)),
		))
	}
	if len(p.agents) == 0 {
		lines = append(lines, "  none")
	}
	content := workflowBoundedContent(strings.Join(lines, "\n"), p.width, p.height)
	return styles.BaseStyle().Width(p.width).Height(p.height).Render(content)
}

func (p *workflowPage) GetSize() (int, int) { return p.width, p.height }
func (p *workflowPage) SetSize(width, height int) tea.Cmd {
	p.width, p.height = max(0, width), max(0, height)
	return nil
}
func (p *workflowPage) BindingKeys() []key.Binding { return []key.Binding{p.refresh} }

var _ Lifecycle = (*workflowPage)(nil)

// workflowField keeps each persisted value within its intended row and strips
// control sequences before any layout or styling code sees it.
func workflowField(value string) string {
	return strings.Join(strings.Fields(styles.SanitizeTerminalText(value)), " ")
}

func workflowBoundedContent(value string, width, height int) string {
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
