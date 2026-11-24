package page

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

// workspaceHeader is deliberately a single, information-dense line. It gives
// the user the current context without turning the workspace into a dashboard.
func workspaceHeader(width int, a *app.App, s session.Session) string {
	if width <= 0 {
		return ""
	}
	t := theme.CurrentTheme()
	base := styles.BaseStyle()
	project := workspaceLabel(filepath.Base(config.WorkingDirectory()), "project")

	state := "ready"
	stateColor := t.TextMuted()
	stateGlyph := styles.CheckGlyph()
	if a != nil && a.CoderAgent != nil && s.ID != "" && a.CoderAgent.IsSessionBusy(s.ID) {
		state = "working"
		stateColor = t.Primary()
		stateGlyph = styles.LoadingGlyph()
	}

	brandText := styles.OpenCodeGlyph() + " RSCH"
	brand := base.Bold(true).Foreground(t.Primary()).Render(brandText)
	statusText := stateGlyph + " " + state
	status := base.Foreground(stateColor).Render(statusText)
	right := status

	if width >= layout.SidebarWidth {
		modelLabel, providerLabel := workspaceModelLabels(a)
		separator := " " + styles.Glyph("·", "|") + " "
		metaParts := []string{
			truncateWorkspaceLabel(modelLabel, 24),
			truncateWorkspaceLabel(providerLabel, 18),
		}
		if s.ID != "" {
			sessionTitle := workspaceLabel(s.Title, "untitled session")
			metaParts = append(metaParts, truncateWorkspaceLabel(sessionTitle, 24))
		}
		meta := base.Foreground(t.TextMuted()).Render(strings.Join(metaParts, separator))
		right = meta + "  " + status
	} else if width >= layout.CompactWidth {
		right = base.Foreground(t.TextMuted()).Render("session "+sessionState(s)+"  ") + right
	}

	brandWidth := lipgloss.Width(brand)
	rightWidth := lipgloss.Width(right)
	if rightWidth+brandWidth+1 > width {
		right = status
		rightWidth = lipgloss.Width(right)
	}
	if rightWidth+brandWidth+1 > width {
		compact := brand + " " + status
		return base.Width(width).MaxWidth(width).Render(ansi.Truncate(compact, width, ""))
	}

	availableLeft := width - rightWidth - 1
	left := brand
	if projectWidth := availableLeft - brandWidth - 2; projectWidth > 0 {
		project = truncateWorkspaceLabel(project, projectWidth)
		left += base.Foreground(t.TextMuted()).Render("  " + project)
	}

	gap := max(1, width-lipgloss.Width(left)-rightWidth)
	line := left + strings.Repeat(" ", gap) + right
	return base.Width(width).MaxWidth(width).Render(ansi.Truncate(line, width, ""))
}

func workspaceModelLabels(a *app.App) (string, string) {
	var modelLabel, providerLabel string
	if a != nil && a.CoderAgent != nil {
		model := a.CoderAgent.Model()
		modelLabel = model.Name
		if strings.TrimSpace(modelLabel) == "" {
			modelLabel = string(model.ID)
		}
		providerLabel = string(model.Provider)
	}
	if strings.TrimSpace(modelLabel) == "" && strings.TrimSpace(providerLabel) == "" {
		if cfg := config.Get(); cfg != nil {
			if coder, ok := cfg.Agents[config.AgentCoder]; ok {
				if model, found := models.SupportedModels[coder.Model]; found {
					modelLabel = model.Name
					providerLabel = string(model.Provider)
				}
			}
		}
	}
	return workspaceLabel(modelLabel, "model unavailable"), workspaceLabel(providerLabel, "provider unavailable")
}

func workspaceLabel(value, fallback string) string {
	value = strings.Join(strings.Fields(styles.SanitizeTerminalText(value)), " ")
	if value == "" || value == "." || value == string(filepath.Separator) {
		return fallback
	}
	return value
}

func truncateWorkspaceLabel(value string, width int) string {
	if width <= 0 {
		return ""
	}
	ellipsis := styles.Glyph("…", "...")
	if lipgloss.Width(ellipsis) > width {
		ellipsis = ""
	}
	return ansi.Truncate(value, width, ellipsis)
}

func sessionState(s session.Session) string {
	if s.ID == "" {
		return "new"
	}
	return fmt.Sprintf("%d msg", s.MessageCount)
}
