package styles

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

type glyphCase struct {
	name    string
	render  func() string
	unicode string
	ascii   string
}

func semanticGlyphCases() []glyphCase {
	return []glyphCase{
		{name: "brand", render: OpenCodeGlyph, unicode: "⌬", ascii: "#"},
		{name: "check", render: CheckGlyph, unicode: "✓", ascii: "+"},
		{name: "error", render: ErrorGlyph, unicode: "×", ascii: "x"},
		{name: "warning", render: WarningGlyph, unicode: "▲", ascii: "!"},
		{name: "info", render: InfoGlyph, unicode: "i", ascii: "i"},
		{name: "hint", render: HintGlyph, unicode: "?", ascii: "?"},
		{name: "spinner", render: SpinnerGlyph, unicode: "·", ascii: "."},
		{name: "loading", render: LoadingGlyph, unicode: "↻", ascii: "~"},
		{name: "document", render: DocumentGlyph, unicode: "▣", ascii: "@"},
	}
}

func setUnicodeTerminal(t *testing.T) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("RSCH_ASCII", "")
}

func TestSemanticGlyphsUseOneCellUnicode(t *testing.T) {
	setUnicodeTerminal(t)
	for _, test := range semanticGlyphCases() {
		t.Run(test.name, func(t *testing.T) {
			if got := test.render(); got != test.unicode {
				t.Fatalf("glyph = %q, want Unicode %q", got, test.unicode)
			}
			if width := ansi.StringWidth(test.render()); width != 1 {
				t.Fatalf("glyph %q has width %d, want 1", test.render(), width)
			}
		})
	}
}

func TestSemanticGlyphsHonorRSCHASCII(t *testing.T) {
	setUnicodeTerminal(t)
	t.Setenv("RSCH_ASCII", "1")
	for _, test := range semanticGlyphCases() {
		t.Run(test.name, func(t *testing.T) {
			if got := test.render(); got != test.ascii {
				t.Fatalf("glyph = %q, want ASCII %q", got, test.ascii)
			}
			if len(test.render()) != 1 || ansi.StringWidth(test.render()) != 1 {
				t.Fatalf("fallback %q is not one ASCII cell", test.render())
			}
		})
	}
}

func TestSemanticGlyphsUseASCIIForDumbTerminal(t *testing.T) {
	setUnicodeTerminal(t)
	t.Setenv("TERM", "dumb")
	for _, test := range semanticGlyphCases() {
		t.Run(test.name, func(t *testing.T) {
			if got := test.render(); got != test.ascii {
				t.Fatalf("glyph = %q, want dumb-terminal fallback %q", got, test.ascii)
			}
		})
	}
}

func TestOneCellGlyphRejectsUnstableDefinitions(t *testing.T) {
	setUnicodeTerminal(t)
	if got := OneCellGlyph("🖼", "@"); got != "@" {
		t.Fatalf("wide emoji glyph = %q, want safe fallback", got)
	}
	if got := OneCellGlyph("ab", "xx"); got != "?" {
		t.Fatalf("invalid glyph definitions = %q, want defensive fallback", got)
	}
}

func TestLegacyIconsRemainOneCellStrings(t *testing.T) {
	icons := map[string]string{
		"brand": OpenCodeIcon, "check": CheckIcon, "error": ErrorIcon,
		"warning": WarningIcon, "info": InfoIcon, "hint": HintIcon,
		"spinner": SpinnerIcon, "loading": LoadingIcon, "document": DocumentIcon,
	}
	for name, icon := range icons {
		if width := ansi.StringWidth(icon); width != 1 {
			t.Errorf("%s icon %q has width %d, want 1", name, icon, width)
		}
	}
}
