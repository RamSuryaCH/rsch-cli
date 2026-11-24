package diff

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

func TestFormatDiffPreservesUnicodeDuringIntralineHighlighting(t *testing.T) {
	input := "--- a/example.txt\n+++ b/example.txt\n@@ -1 +1 @@\n-café 世界 👩🏽‍💻\n+cafés 世界 👩🏽‍💻\n"
	got, err := FormatDiff(input, WithTotalWidth(100))
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(got)
	if !utf8.ValidString(plain) {
		t.Fatalf("formatted diff is invalid UTF-8: %q", plain)
	}
	for _, want := range []string{"café", "cafés", "世界", "👩🏽‍💻"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("formatted diff lost %q: %q", want, plain)
		}
	}
}

func TestFormatDiffRendersFileAndHunkOrientation(t *testing.T) {
	input := "--- a/old.txt\n+++ b/new.txt\n@@ -10,2 +10,2 @@ func example\n-old\n+new\n context\n"
	got, err := FormatDiff(input, WithTotalWidth(80))
	if err != nil {
		t.Fatal(err)
	}

	plain := ansi.Strip(got)
	for _, want := range []string{"File: old.txt → new.txt", "@@ -10,2 +10,2 @@ func example"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("formatted diff omitted orientation %q: %q", want, plain)
		}
	}
}

func TestParseUnifiedDiffIgnoresNoNewlineMarkerAndTrailingNewline(t *testing.T) {
	input := "--- a/example.txt\n+++ b/example.txt\n@@ -1 +1 @@\n-old\n+new\n\\ No newline at end of file\n"
	result, err := ParseUnifiedDiff(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(result.Hunks))
	}
	if got := len(result.Hunks[0].Lines); got != 2 {
		t.Fatalf("hunk lines = %d, want 2; marker or trailing newline became content", got)
	}
}

func TestApplyHighlightingResetsForegroundAndBackground(t *testing.T) {
	const originalForeground = "\x1b[38;2;10;20;30m"
	got := applyHighlighting(
		originalForeground+"changed",
		[]Segment{{Start: 0, End: 1, Type: LineAdded, Text: "c"}},
		LineAdded,
		theme.CurrentTheme().DiffHighlightAdded(),
	)

	reset := "\x1b[39;49m" + originalForeground
	if !strings.Contains(got, reset) {
		t.Fatalf("highlighted output does not reset both colors before restoring syntax style: %q", got)
	}
	if strings.Index(got, reset) > strings.Index(got, "h") {
		t.Fatalf("background reset occurs after unhighlighted content: %q", got)
	}
}
