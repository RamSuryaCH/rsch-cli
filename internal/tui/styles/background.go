package styles

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func getColorRGB(c lipgloss.TerminalColor) (uint8, uint8, uint8) {
	r, g, b, a := c.RGBA()

	// Un-premultiply alpha if needed
	if a > 0 && a < 0xffff {
		r = (r * 0xffff) / a
		g = (g * 0xffff) / a
		b = (b * 0xffff) / a
	}

	// Convert from 16-bit to 8-bit color
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

// ForceReplaceBackgroundWithLipgloss replaces any ANSI background color codes
// in `input` with a single 24‑bit background (48;2;R;G;B).
func ForceReplaceBackgroundWithLipgloss(input string, newBgColor lipgloss.TerminalColor) string {
	if os.Getenv("NO_COLOR") != "" || ASCIIOnly() {
		return xansi.Strip(input)
	}
	// Precompute our new-bg sequence once
	r, g, b := getColorRGB(newBgColor)
	newBg := fmt.Sprintf("48;2;%d;%d;%d", r, g, b)

	return ansiEscape.ReplaceAllStringFunc(input, func(seq string) string {
		const (
			escPrefixLen = 2 // "\x1b["
			escSuffixLen = 1 // "m"
		)

		raw := seq
		start := escPrefixLen
		end := len(raw) - escSuffixLen

		tokens := strings.Split(raw[start:end], ";")
		kept := make([]string, 0, len(tokens)+1)
		for i := 0; i < len(tokens); {
			token := tokens[i]
			value, numeric := strconv.Atoi(token)
			if numeric == nil && value == 48 {
				if i+1 < len(tokens) && tokens[i+1] == "5" && i+2 < len(tokens) {
					i += 3
					continue
				}
				if i+1 < len(tokens) && tokens[i+1] == "2" && i+4 < len(tokens) {
					i += 5
					continue
				}
				i++
				continue
			}
			if numeric == nil && ((value >= 40 && value <= 47) || (value >= 100 && value <= 107) || value == 49) {
				i++
				continue
			}
			// Preserve extended foreground groups atomically. Color values such as
			// 48 or 100 are data here, not background opcodes.
			if numeric == nil && value == 38 && i+1 < len(tokens) {
				groupLength := 1
				if tokens[i+1] == "5" && i+2 < len(tokens) {
					groupLength = 3
				} else if tokens[i+1] == "2" && i+4 < len(tokens) {
					groupLength = 5
				}
				kept = append(kept, tokens[i:i+groupLength]...)
				i += groupLength
				continue
			}
			kept = append(kept, token)
			i++
		}
		kept = append(kept, newBg)
		return "\x1b[" + strings.Join(kept, ";") + "m"
	})
}
