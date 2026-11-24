package page

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	appmodel "github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestWorkspaceHeaderResponsive(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120, 148, 160} {
		header := workspaceHeader(width, nil, session.Session{})
		if strings.TrimSpace(ansi.Strip(header)) == "" {
			t.Fatalf("header is visually empty at width %d", width)
		}
		if lipgloss.Width(header) > width {
			t.Fatalf("header width %d exceeds terminal width %d", lipgloss.Width(header), width)
		}
		if lipgloss.Height(header) != 1 {
			t.Fatalf("header height = %d, want 1 at width %d", lipgloss.Height(header), width)
		}
	}
}

type workspaceHeaderAgent struct {
	stubChatAgent
	model        models.Model
	busy         map[string]bool
	globallyBusy bool
}

func (a *workspaceHeaderAgent) Model() models.Model { return a.model }
func (a *workspaceHeaderAgent) IsSessionBusy(sessionID string) bool {
	return a.busy[sessionID]
}
func (a *workspaceHeaderAgent) IsBusy() bool { return a.globallyBusy }

func TestWorkspaceHeaderUsesCurrentSessionBusyState(t *testing.T) {
	agent := &workspaceHeaderAgent{
		busy:         map[string]bool{"other-session": true},
		globallyBusy: true,
	}
	a := &appmodel.App{CoderAgent: agent}
	s := session.Session{ID: "current-session"}

	if plain := ansi.Strip(workspaceHeader(80, a, s)); !strings.Contains(plain, "ready") || strings.Contains(plain, "working") {
		t.Fatalf("unrelated activity changed current session state: %q", plain)
	}

	agent.busy[s.ID] = true
	if plain := ansi.Strip(workspaceHeader(80, a, s)); !strings.Contains(plain, "working") {
		t.Fatalf("current session activity is not visible: %q", plain)
	}
}

func TestWorkspaceHeaderSanitizesExternalLabels(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "pro\x1b[2J\nject")
	if err := os.Mkdir(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectDir)

	agent := &workspaceHeaderAgent{model: models.Model{
		Name:     "Mo\x1b]0;owned\a\ndel",
		Provider: models.ModelProvider("pro\tvider\u202e"),
	}}
	s := session.Session{ID: "session-1", Title: "Sess\nion\x1b[2J"}
	plain := ansi.Strip(workspaceHeader(160, &appmodel.App{CoderAgent: agent}, s))

	for _, want := range []string{"pro ject", "Mo del", "pro vider", "Sess ion"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("sanitized label %q is not visible in %q", want, plain)
		}
	}
	if strings.ContainsAny(plain, "\n\r\t\x1b") || strings.ContainsRune(plain, '\u202e') {
		t.Fatalf("workspace header contains injected terminal controls: %q", plain)
	}
}

func TestWorkspaceHeaderUsesSemanticASCIIGlyphs(t *testing.T) {
	t.Setenv("RSCH_ASCII", "1")
	agent := &workspaceHeaderAgent{model: models.Model{Name: "model", Provider: "provider"}}
	plain := ansi.Strip(workspaceHeader(160, &appmodel.App{CoderAgent: agent}, session.Session{}))

	for _, want := range []string{"# RSCH", "+ ready", "model | provider"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("ASCII header omitted %q: %q", want, plain)
		}
	}
	if strings.ContainsAny(plain, "⌬✓↻·…") {
		t.Fatalf("ASCII header contains Unicode-only glyphs: %q", plain)
	}
}

func TestChatPageKeepsHeaderAfterCompactResize(t *testing.T) {
	p := NewChatPage(nil).(*chatPage)
	p.SetSize(80, 23)
	view := ansi.Strip(p.View())
	if !strings.Contains(view, "RSCH") {
		t.Fatalf("compact chat view does not contain workspace header:\n%s", view)
	}
}
