package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestSidebarSanitizesSessionAndModifiedPathLabels(t *testing.T) {
	hostile := "safe\x1b]0;spoofed\a\x1b[2J\u202eevil\tlabel"
	sidebar := &sidebarCmp{
		width:   48,
		session: session.Session{Title: hostile},
	}

	for name, view := range map[string]string{
		"session": sidebar.sessionSection(),
		"path":    sidebar.modifiedFile(hostile+".go", 12, 3),
	} {
		t.Run(name, func(t *testing.T) {
			assertSidebarHasNoInjectedControls(t, view)
			if plain := ansi.Strip(view); !strings.Contains(plain, "safeevil label") {
				t.Fatalf("sanitized label is not visible: %q", plain)
			}
			if got := lipgloss.Width(view); got > sidebar.width {
				t.Fatalf("rendered width = %d, want <= %d", got, sidebar.width)
			}
		})
	}
}

func TestSidebarSanitizesLSPNameAndCommand(t *testing.T) {
	hostileName := "go\x1b]0;spoofed\a\u202els"
	hostileCommand := "safe\x1b[2J\u2066gopls\tserve"
	view := renderSafeLSPsConfigured(48, map[string]config.LSPConfig{
		hostileName: {Command: hostileCommand},
	})

	assertSidebarHasNoInjectedControls(t, view)
	plain := ansi.Strip(view)
	for _, expected := range []string{"gols", "safegopls serve"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("sanitized LSP label %q is not visible: %q", expected, plain)
		}
	}
	if got := lipgloss.Width(view); got > 48 {
		t.Fatalf("rendered width = %d, want <= 48", got)
	}
}

func assertSidebarHasNoInjectedControls(t *testing.T, value string) {
	t.Helper()
	for _, unsafe := range []string{"\x1b]", "\a", "\x1b[2J", "\r", "\b", "\t", "\u202e", "\u2066"} {
		if strings.Contains(value, unsafe) {
			t.Fatalf("rendered value contains unsafe sequence %q: %q", unsafe, value)
		}
	}
}
