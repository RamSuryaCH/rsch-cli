package styles

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeTerminalTextRemovesTerminalAndBidiControls(t *testing.T) {
	input := "safe\x1b]52;c;c2VjcmV0\a\x1b[2J\x1b]0;spoofed\a\r\b\u202eevil\u2066 text\n\t世界"
	got := SanitizeTerminalText(input)

	if strings.ContainsAny(got, "\x1b\a\r\b") {
		t.Fatalf("sanitized output retains terminal controls: %q", got)
	}
	for _, bidi := range []string{"\u202e", "\u2066"} {
		if strings.Contains(got, bidi) {
			t.Fatalf("sanitized output retains bidi control %q: %q", bidi, got)
		}
	}
	if !strings.Contains(got, "safeevil text\n\t世界") {
		t.Fatalf("sanitized output lost ordinary text: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized output is not valid UTF-8: %q", got)
	}
}

func TestSanitizeTerminalTextRepairsInvalidUTF8(t *testing.T) {
	got := SanitizeTerminalText(string([]byte{'a', 0xff, 'b'}))
	if got != "a�b" {
		t.Fatalf("SanitizeTerminalText() = %q, want %q", got, "a�b")
	}
}
