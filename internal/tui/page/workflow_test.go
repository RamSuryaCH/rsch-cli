package page

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	appmodel "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestWorkflowRefreshLifecycleKeepsOneCadence(t *testing.T) {
	loads := 0
	var ticks []workflowTickMsg
	model := NewWorkflowPage(nil).(*workflowPage)
	model.loadSnapshot = func(context.Context, *appmodel.App) (workflowSnapshot, error) {
		loads++
		return workflowSnapshot{
			goals:         []engineering.Goal{{ID: "goal-1"}},
			evidenceCount: map[string]int{},
			lastRefresh:   time.Unix(20, 0),
		}, nil
	}
	model.newTick = func(msg workflowTickMsg) tea.Cmd {
		ticks = append(ticks, msg)
		return func() tea.Msg { return msg }
	}

	initCmd := model.Init()
	if loads != 0 {
		t.Fatal("workflow loader ran synchronously during Init")
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
	if len(model.goals) != 0 {
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
	if len(model.goals) != 1 || model.goals[0].ID != "goal-1" {
		t.Fatalf("loaded workflow snapshot was not applied: %#v", model.goals)
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
		t.Fatal("deactivation did not cancel the workflow refresh lifetime")
	}
	model.Update(staleTick)
	if len(ticks) != 2 {
		t.Fatalf("hidden workflow continued its cadence: %d", len(ticks))
	}
	model.Activate()
	if len(ticks) != 3 {
		t.Fatalf("reactivation cadence count = %d, want 3", len(ticks))
	}
}

func TestWorkflowSanitizesExternalFieldsAndBoundsOutput(t *testing.T) {
	const hostile = "safe\x1b]52;c;c2VjcmV0\a\x1b[2J\u202eevil\nnext"
	model := &workflowPage{
		width:  48,
		height: 24,
		goals: []engineering.Goal{{
			ID: hostile, Title: hostile, Status: engineering.Status(hostile),
		}},
		tasks: []engineering.Task{{
			ID: hostile, Title: hostile, Status: engineering.Status(hostile), Command: []string{hostile}, Verification: []string{"one"},
		}},
		evidenceCount: map[string]int{hostile: 1},
		agents: []orchestration.Agent{{
			ID: hostile, Role: hostile, Status: orchestration.AgentStatus(hostile),
		}},
		sessions: []session.Session{{ID: hostile, Title: hostile}},
	}

	view := model.View()
	for _, forbidden := range []string{"\x1b]", "\a", "\x1b[2J", "\u202e", "c2VjcmV0"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("workflow retained terminal payload %q: %q", forbidden, view)
		}
	}
	if got := lipgloss.Width(view); got > model.width {
		t.Fatalf("workflow width = %d, want <= %d", got, model.width)
	}
	if got := lipgloss.Height(view); got > model.height {
		t.Fatalf("workflow height = %d, want <= %d", got, model.height)
	}
	if got := workflowField(hostile); got != "safeevil next" {
		t.Fatalf("workflowField() = %q, want sanitized single-line text", got)
	}
}

func TestWorkflowBoundsNarrowShortView(t *testing.T) {
	model := &workflowPage{
		width:  18,
		height: 6,
		goals: []engineering.Goal{{
			ID:    "goal-with-a-very-long-identifier",
			Title: "a very long goal title that cannot fit",
		}},
	}
	view := model.View()
	if got := lipgloss.Width(view); got > model.width {
		t.Fatalf("workflow width = %d, want <= %d", got, model.width)
	}
	if got := lipgloss.Height(view); got > model.height {
		t.Fatalf("workflow height = %d, want <= %d", got, model.height)
	}
}
