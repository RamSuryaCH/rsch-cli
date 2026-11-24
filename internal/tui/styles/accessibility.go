package styles

import (
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// ASCIIOnly reports whether terminal glyphs should avoid Unicode. Users can
// force this with RSCH_ASCII, while TERM=dumb and explicitly non-UTF-8 C/POSIX
// locales receive a conservative fallback automatically.
func ASCIIOnly() bool {
	if value := strings.TrimSpace(os.Getenv("RSCH_ASCII")); value != "" {
		enabled, err := strconv.ParseBool(value)
		return err != nil || enabled
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return true
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		locale := strings.TrimSpace(os.Getenv(name))
		if locale == "" {
			continue
		}
		return strings.EqualFold(locale, "C") || strings.EqualFold(locale, "POSIX")
	}
	return false
}

// Glyph chooses a Unicode glyph or its portable ASCII equivalent.
func Glyph(unicode, ascii string) string {
	if ASCIIOnly() {
		return ascii
	}
	return unicode
}

// OneCellGlyph chooses a glyph that occupies exactly one terminal cell. The
// ASCII fallback must be one printable ASCII byte; malformed definitions fall
// back to "?" instead of destabilizing alignment in lists, tables, and status
// lines. Unlike the legacy icon variables, this function evaluates the
// environment on every call, which is useful for renderers and tests that
// change terminal capabilities at runtime.
func OneCellGlyph(unicodeGlyph, asciiGlyph string) string {
	if ASCIIOnly() {
		if isPrintableASCIICell(asciiGlyph) {
			return asciiGlyph
		}
		return "?"
	}
	if isOneCell(unicodeGlyph) {
		return unicodeGlyph
	}
	if isPrintableASCIICell(asciiGlyph) {
		return asciiGlyph
	}
	return "?"
}

func isOneCell(value string) bool {
	if value == "" || ansi.StringWidth(value) != 1 {
		return false
	}
	for _, r := range value {
		// Supplementary-plane symbols are frequently rendered by terminal emoji
		// fonts with a width that disagrees with Unicode width tables. Keep the
		// semantic icon set in the BMP for predictable cell accounting.
		if unicode.IsControl(r) || r > '\uFFFF' {
			return false
		}
	}
	return true
}

func isPrintableASCIICell(value string) bool {
	return len(value) == 1 && value[0] >= ' ' && value[0] <= '~'
}

// SelectionMarker is a visible, non-color selection cue. It remains distinct
// when NO_COLOR strips foreground and background colors.
func SelectionMarker(selected bool) string {
	if selected {
		return Glyph("› ", "> ")
	}
	return "  "
}

// SelectionLabel prefixes list content with a stable-width selection marker.
func SelectionLabel(label string, selected bool) string {
	return SelectionMarker(selected) + label
}
