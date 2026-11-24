package styles

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// SanitizeTerminalText removes terminal control sequences from untrusted text.
// Model output, tool output, filenames, URLs, and permission descriptions can
// all originate outside the TUI. They must never be able to move the cursor,
// clear the screen, set the window title, or write to the clipboard.
func SanitizeTerminalText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	value = ansi.Strip(value)

	var clean strings.Builder
	clean.Grow(len(value))
	for _, r := range value {
		switch r {
		case '\n', '\t':
			clean.WriteRune(r)
		default:
			if unicode.IsControl(r) || isBidiControl(r) {
				continue
			}
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

func isBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') ||
		(r >= '\u2066' && r <= '\u2069')
}
