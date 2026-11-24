package styles

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestGlyphUsesASCIIFallback(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("RSCH_ASCII", "1")

	if got := Glyph("› ", "> "); got != "> " {
		t.Fatalf("Glyph() = %q, want ASCII fallback", got)
	}
}

func TestGlyphUsesASCIIForDumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("RSCH_ASCII", "")

	if got := Glyph("✓", "ok"); got != "ok" {
		t.Fatalf("Glyph() = %q, want dumb-terminal fallback", got)
	}
}

func TestSelectionMarkerSurvivesNoColor(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("RSCH_ASCII", "")
	t.Setenv("NO_COLOR", "1")

	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.Ascii)
	selected := renderer.NewStyle().
		Foreground(lipgloss.Color("#ffffff")).
		Background(lipgloss.Color("#000000")).
		Render(SelectionLabel("allow", true))
	unselected := renderer.NewStyle().
		Foreground(lipgloss.Color("#000000")).
		Background(lipgloss.Color("#ffffff")).
		Render(SelectionLabel("allow", false))
	if selected == unselected {
		t.Fatalf("selection labels must differ without relying on color: %q", selected)
	}
	if selected != "› allow" {
		t.Fatalf("SelectionLabel() = %q, want visible marker", selected)
	}
	if strings.Contains(selected+unselected, "\x1b[") {
		t.Fatalf("ASCII color profile emitted ANSI color: %q %q", selected, unselected)
	}
}
