package styles

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestForceReplaceBackgroundPreservesExtendedForegroundGroups(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("RSCH_ASCII", "")
	t.Setenv("NO_COLOR", "")

	input := "\x1b[38;2;48;64;80;48;5;100;1mtext\x1b[0m"
	got := ForceReplaceBackgroundWithLipgloss(input, lipgloss.Color("#010203"))
	if !strings.Contains(got, "38;2;48;64;80") {
		t.Fatalf("foreground RGB group was corrupted: %q", got)
	}
	if strings.Contains(got, "48;5;100") {
		t.Fatalf("old background group was retained: %q", got)
	}
	if !strings.Contains(got, "48;2;") {
		t.Fatalf("replacement background missing: %q", got)
	}
}

func TestForceReplaceBackgroundHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	got := ForceReplaceBackgroundWithLipgloss("\x1b[31mred\x1b[0m", lipgloss.Color("#010203"))
	if got != "red" {
		t.Fatalf("NO_COLOR output = %q, want plain text", got)
	}
}
