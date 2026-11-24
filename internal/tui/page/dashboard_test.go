package page

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	appmodel "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/gitsafety"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/research"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestDashboardRefreshLifecycleKeepsOneCadence(t *testing.T) {
	loads := 0
	var ticks []dashboardTickMsg
	model := NewDashboardPage(nil).(*dashboardPage)
	model.loadSnapshot = func(context.Context, *appmodel.App) (dashboardSnapshot, error) {
		loads++
		return dashboardSnapshot{agents: []string{"agent-ready"}, refreshed: time.Unix(10, 0)}, nil
	}
	model.newTick = func(msg dashboardTickMsg) tea.Cmd {
		ticks = append(ticks, msg)
		return func() tea.Msg { return msg }
	}

	initCmd := model.Init()
	if loads != 0 {
		t.Fatal("dashboard loader ran synchronously during Init")
	}
	if len(ticks) != 1 {
		t.Fatalf("initial cadence count = %d, want 1", len(ticks))
	}

	batch, ok := initCmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("initial command = %#v, want refresh and cadence batch", initCmd())
	}
	loaded := batch[0]()
	if loads != 1 {
		t.Fatalf("loader calls = %d, want 1 after executing refresh command", loads)
	}
	if len(model.agents) != 0 {
		t.Fatal("background loader mutated the model before Update")
	}

	_, manualCmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if manualCmd != nil || !model.refreshPending {
		t.Fatal("manual refresh did not coalesce behind the in-flight load")
	}
	if len(ticks) != 1 {
		t.Fatalf("manual refresh created a second cadence: %d", len(ticks))
	}

	_, followup := model.Update(loaded)
	if len(model.agents) != 1 || model.agents[0] != "agent-ready" {
		t.Fatalf("loaded dashboard snapshot was not applied: %#v", model.agents)
	}
	if followup == nil {
		t.Fatal("coalesced manual refresh was not replayed")
	}
	secondLoaded := followup()
	if loads != 2 {
		t.Fatalf("loader calls after coalesced refresh = %d, want 2", loads)
	}
	model.Update(secondLoaded)

	firstTick := ticks[0]
	model.Update(firstTick)
	if len(ticks) != 2 {
		t.Fatalf("cadence did not advance exactly once: %d", len(ticks))
	}
	model.Update(firstTick)
	if len(ticks) != 2 {
		t.Fatalf("duplicate tick created an extra cadence: %d", len(ticks))
	}

	staleTick := ticks[1]
	refreshCtx := model.refreshCtx
	model.Deactivate()
	select {
	case <-refreshCtx.Done():
	default:
		t.Fatal("deactivation did not cancel the dashboard refresh lifetime")
	}
	model.Update(staleTick)
	if len(ticks) != 2 {
		t.Fatalf("hidden dashboard continued its cadence: %d", len(ticks))
	}
	model.Activate()
	if len(ticks) != 3 {
		t.Fatalf("reactivation cadence count = %d, want 3", len(ticks))
	}
}

func TestDashboardRendersResponsiveOperationalViews(t *testing.T) {
	model := NewDashboardPage(nil)
	model, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := model.View()
	if !strings.Contains(view, "Operational dashboard") || !strings.Contains(view, "1 agents") {
		t.Fatalf("unexpected dashboard: %q", view)
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	view = model.View()
	if !strings.Contains(view, "workspace") {
		t.Fatalf("workspace view was not selectable: %q", view)
	}
}

func TestDashboardShowsOnlySafeWorkflowAffordances(t *testing.T) {
	model := &dashboardPage{
		active:    1,
		taskItems: []engineering.Task{{Status: engineering.StatusRunning}},
	}
	footer := model.footer()
	for _, action := range []string{"y retry", "p pause"} {
		if !strings.Contains(footer, action) {
			t.Fatalf("running task footer omitted %q: %q", action, footer)
		}
	}
	if strings.Contains(footer, "u resume") {
		t.Fatalf("running task exposed unsafe resume action: %q", footer)
	}

	model.taskItems[0].Status = engineering.StatusPaused
	footer = model.footer()
	if !strings.Contains(footer, "u resume") || strings.Contains(footer, "p pause") {
		t.Fatalf("paused task affordances were not bounded: %q", footer)
	}
}

func TestDashboardHasNoDirectPermissionShortcuts(t *testing.T) {
	model := &dashboardPage{}
	for _, binding := range model.BindingKeys() {
		for _, shortcut := range binding.Keys() {
			if shortcut == "a" || shortcut == "d" {
				t.Fatalf("dashboard exposes direct permission shortcut %q", shortcut)
			}
		}
	}

	footer := model.footer()
	if strings.Contains(footer, "approve") || strings.Contains(footer, "deny") {
		t.Fatalf("dashboard advertises a direct permission decision: %q", footer)
	}
}

func TestDashboardKeepsSelectedTaskVisible(t *testing.T) {
	model := NewDashboardPage(nil).(*dashboardPage)
	model.SetSize(80, 12)
	model.active = 1
	for i := range 20 {
		model.taskItems = append(model.taskItems, engineering.Task{
			ID: fmt.Sprintf("task-%02d", i), Title: fmt.Sprintf("Task %02d", i), Status: engineering.StatusRunning,
		})
	}
	for range 18 {
		model.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := model.View()
	if !strings.Contains(view, "> running  Task 18") {
		t.Fatalf("selected task is not visible after navigation:\n%s", view)
	}
	if !strings.Contains(view, "p pause") {
		t.Fatalf("selected task actions are not visible:\n%s", view)
	}
	assertDashboardSafeAndBounded(t, view, 80, 12)
	for range 2 {
		model.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if view := model.View(); !strings.Contains(view, "> running  Task 00") {
		t.Fatalf("wrapped selection is not visible:\n%s", view)
	}
}

func TestDashboardRefreshPreservesSelectedTaskIdentity(t *testing.T) {
	model := NewDashboardPage(nil).(*dashboardPage)
	model.taskItems = []engineering.Task{{ID: "first"}, {ID: "chosen"}, {ID: "last"}}
	model.selected = 1
	model.applySnapshot(dashboardSnapshot{taskItems: []engineering.Task{
		{ID: "inserted"}, {ID: "first"}, {ID: "chosen"}, {ID: "last"},
	}})
	if got := model.taskItems[model.selected].ID; got != "chosen" {
		t.Fatalf("refresh changed selection to %q, want chosen", got)
	}
	model.applySnapshot(dashboardSnapshot{taskItems: []engineering.Task{{ID: "chosen"}}})
	if model.selected != 0 {
		t.Fatalf("selection after removal = %d, want 0", model.selected)
	}
	model.applySnapshot(dashboardSnapshot{})
	if model.selected != 0 {
		t.Fatalf("empty snapshot left invalid selection %d", model.selected)
	}
}

func TestDashboardSanitizesExternalFieldsAndBoundsOutput(t *testing.T) {
	const hostile = "safe\x1b]52;c;c2VjcmV0\a\x1b[2J\u202eevil\nnext"
	model := &dashboardPage{
		width:     48,
		height:    12,
		views:     []string{"agents", "tasks", "memory", "research", "sources", "sessions", "workspace"},
		agents:    []string{hostile},
		taskItems: []engineering.Task{{ID: hostile, Title: hostile, Status: engineering.Status(hostile), Agent: hostile}},
		memory:    []memory.Entry{{ID: hostile, Layer: memory.Layer(hostile), Kind: memory.Kind(hostile), Content: hostile}},
		claims:    []research.SearchResult{{Claim: research.Claim{ID: hostile, Status: hostile, Text: hostile}}},
		sources:   []research.Source{{ID: hostile, URI: hostile, Location: hostile}},
		sessions:  []session.Session{{ID: hostile, Title: hostile}},
		git:       gitsafety.Status{Reason: hostile},
	}

	for active := range model.views {
		model.active = active
		assertDashboardSafeAndBounded(t, model.View(), model.width, model.height)
	}

	model.git = gitsafety.Status{Available: true, Branch: hostile}
	assertDashboardSafeAndBounded(t, model.View(), model.width, model.height)

	if got := dashboardField(hostile); got != "safeevil next" {
		t.Fatalf("dashboardField() = %q, want sanitized single-line text", got)
	}
}

func assertDashboardSafeAndBounded(t *testing.T, view string, width, height int) {
	t.Helper()
	for _, forbidden := range []string{"\x1b]", "\a", "\x1b[2J", "\u202e", "c2VjcmV0"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("dashboard retained terminal payload %q: %q", forbidden, view)
		}
	}
	if got := lipgloss.Width(view); got > width {
		t.Fatalf("dashboard width = %d, want <= %d", got, width)
	}
	if got := lipgloss.Height(view); got > height {
		t.Fatalf("dashboard height = %d, want <= %d", got, height)
	}
}
