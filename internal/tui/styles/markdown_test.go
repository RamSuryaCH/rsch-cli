package styles

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	xansi "github.com/charmbracelet/x/ansi"
)

var markdownSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestMarkdownRendererContainsEveryVisibleLine(t *testing.T) {
	longURL := "https://example.test/" + strings.Repeat("deeply-nested-segment/", 12) + "reference.html?query=" + strings.Repeat("value", 20)
	longCode := "const generatedValue = \"" + strings.Repeat("abcdefghijklmnopqrstuvwxyz", 12) + "\""
	tests := []struct {
		name     string
		markdown string
		keeps    []string
	}{
		{
			name:     "unbroken prose",
			markdown: strings.Repeat("unbreakable", 60),
			keeps:    []string{"unbreakableunbreakable"},
		},
		{
			name:     "long URL",
			markdown: "Read [the reference](" + longURL + ") before continuing.",
			keeps:    []string{"the reference", longURL},
		},
		{
			name:     "wide Unicode and emoji",
			markdown: strings.Repeat("世界👩🏽‍💻café", 40),
			keeps:    []string{"世界👩🏽‍💻café"},
		},
		{
			name:     "tabbed fenced code",
			markdown: "```go\n\t" + longCode + "\n\tfmt.Println(generatedValue)\n```",
			keeps:    []string{"generatedValue", "fmt.Println"},
		},
	}

	for _, width := range []int{2, 4, 8, 17, 40, 80} {
		for _, tc := range tests {
			t.Run(tc.name+"/width="+strconv.Itoa(width), func(t *testing.T) {
				rendered, err := GetMarkdownRenderer(width).Render(tc.markdown)
				if err != nil {
					t.Fatalf("Render() error = %v", err)
				}
				assertContainedMarkdown(t, rendered, width)
				if strings.ContainsRune(rendered, '\t') {
					t.Fatalf("rendered Markdown retains a terminal-dependent tab: %q", rendered)
				}

				plainJoined := removeMarkdownLayoutWhitespace(xansi.Strip(rendered))
				for _, keep := range tc.keeps {
					keep = removeMarkdownLayoutWhitespace(keep)
					if !strings.Contains(plainJoined, keep) {
						t.Fatalf("rendered Markdown lost %q: %q", keep, plainJoined)
					}
				}
			})
		}
	}
}

func TestMarkdownRendererContainsWideGraphemesInSingleCellWidth(t *testing.T) {
	rendered, err := GetMarkdownRenderer(1).Render("界👩🏽‍💻界\n```text\n界👩🏽‍💻界\n```")
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertContainedMarkdown(t, rendered, 1)
}

func removeMarkdownLayoutWhitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func TestMarkdownRendererSanitizesBeforeRendering(t *testing.T) {
	input := "safe\x1b]52;c;c2VjcmV0\a\x1b[2J\u202espoof\n" + strings.Repeat("x", 80)
	rendered, err := GetMarkdownRenderer(12).Render(input)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	assertContainedMarkdown(t, rendered, 12)
	plain := xansi.Strip(rendered)
	if strings.Contains(plain, "c2VjcmV0") || strings.ContainsRune(plain, '\u202e') {
		t.Fatalf("rendered Markdown retained hostile terminal content: %q", plain)
	}
	if !strings.Contains(plain, "safe") || !strings.Contains(plain, "spoof") {
		t.Fatalf("rendered Markdown lost surrounding readable content: %q", plain)
	}
}

func TestMarkdownRendererContainsEntityEncodedControls(t *testing.T) {
	for _, input := range []string{
		"safe &#27;[2J after",
		"safe &#x1b;]52;c;c2VjcmV0&#7; after",
		"safe &#x202e;spoof&#x2066; after",
		"[safe](https://example.test/&#27;[2J)",
	} {
		rendered, err := GetMarkdownRenderer(80).Render(input)
		if err != nil {
			t.Fatal(err)
		}
		assertContainedMarkdown(t, rendered, 80)
		if !strings.Contains(xansi.Strip(rendered), "safe") {
			t.Fatalf("readable text lost: %q", rendered)
		}
	}
}

func TestRenderedMarkdownSanitizationPreservesStyling(t *testing.T) {
	input := "\x1b[1;38;2;12;34;56mstyled\x1b[0m\x1b]52;c;c2VjcmV0\a\u202e plain"
	want := "\x1b[1;38;2;12;34;56mstyled\x1b[0m plain"
	if got := sanitizeRenderedMarkdown(input); got != want {
		t.Fatalf("sanitized rendered Markdown = %q, want safe styling %q", got, want)
	}
}

func TestMarkdownRendererUsesASCIISemanticGlyphs(t *testing.T) {
	t.Setenv("RSCH_ASCII", "1")
	rendered, err := GetMarkdownRenderer(80).Render("> quote\n\n- item\n\n- [x] done\n\n---\n\n| a | b |\n| - | - |\n| c | d |")
	if err != nil {
		t.Fatal(err)
	}
	for _, glyph := range []string{"┃", "•", "✓", "─", "┼", "│", "▣", "🖼"} {
		if strings.Contains(rendered, glyph) {
			t.Fatalf("ASCII markdown contains Unicode semantic glyph %q: %q", glyph, rendered)
		}
	}
}

func assertContainedMarkdown(t *testing.T, rendered string, width int) {
	t.Helper()
	if !utf8.ValidString(rendered) {
		t.Fatalf("rendered Markdown is not valid UTF-8: %q", rendered)
	}
	for lineNumber, line := range strings.Split(rendered, "\n") {
		if got := xansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", lineNumber+1, got, width, line)
		}
	}

	// Glamour may emit SGR styling, but no cursor movement, OSC, clipboard,
	// title-setting, bidi, or other terminal controls may survive rendering.
	withoutSGR := markdownSGR.ReplaceAllString(rendered, "")
	for _, r := range withoutSGR {
		if r != '\n' && (unicode.IsControl(r) || isBidiControl(r)) {
			t.Fatalf("rendered Markdown contains prohibited control %U: %q", r, rendered)
		}
	}
}
