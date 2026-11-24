package layout

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	chAnsi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// Most of this code is borrowed from
// https://github.com/charmbracelet/lipgloss/pull/102
// as well as the lipgloss library, with some modification for what I needed.

// Split a string into lines, additionally returning the size of the widest
// line.
func getLines(s string) (lines []string, widest int) {
	lines = strings.Split(s, "\n")

	for _, l := range lines {
		w := chAnsi.StringWidth(l)
		if widest < w {
			widest = w
		}
	}

	return lines, widest
}

// PlaceOverlay places fg on top of bg.
func PlaceOverlay(
	x, y int,
	fg, bg string,
	shadow bool, opts ...WhitespaceOption,
) string {
	fgLines, fgWidth := getLines(fg)
	bgLines, bgWidth := getLines(bg)
	bgHeight := len(bgLines)
	fgHeight := len(fgLines)

	if shadow {
		t := theme.CurrentTheme()
		baseStyle := styles.BaseStyle()

		var shadowbg string = ""
		shadowchar := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Background()).
			Render("░")
		bgchar := baseStyle.Render(" ")
		for i := 0; i <= fgHeight; i++ {
			if i == 0 {
				shadowbg += bgchar + strings.Repeat(bgchar, fgWidth) + "\n"
			} else {
				shadowbg += bgchar + strings.Repeat(shadowchar, fgWidth) + "\n"
			}
		}

		fg = PlaceOverlay(0, 0, fg, shadowbg, false, opts...)
		fgLines, fgWidth = getLines(fg)
		fgHeight = len(fgLines)
	}

	// An overlay is always confined to the background's cell box. In
	// particular, clipping happens before placement so an oversized foreground
	// is anchored at the nearest valid position instead of escaping the frame.
	// ansi.Cut is grapheme- and escape-sequence-aware, so wide Unicode glyphs
	// and styled text are never split in the middle.
	if bgWidth <= 0 || bgHeight <= 0 {
		return bg
	}
	if fgHeight > bgHeight {
		fgLines = fgLines[:bgHeight]
		fgHeight = len(fgLines)
	}
	if fgWidth > bgWidth {
		fgWidth = 0
		for i, line := range fgLines {
			fgLines[i] = chAnsi.Cut(line, 0, bgWidth)
			fgWidth = max(fgWidth, chAnsi.StringWidth(fgLines[i]))
		}
	}

	if fgWidth >= bgWidth && fgHeight >= bgHeight {
		return strings.Join(fgLines, "\n")
	}
	// Restrict overlay placement to the background bounds to avoid terminal clipping or rendering corruption.
	x = util.Clamp(x, 0, bgWidth-fgWidth)
	y = util.Clamp(y, 0, bgHeight-fgHeight)

	ws := &whitespace{}
	for _, opt := range opts {
		opt(ws)
	}

	var b strings.Builder
	for i, bgLine := range bgLines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i < y || i >= y+fgHeight {
			b.WriteString(bgLine)
			continue
		}

		pos := 0
		if x > 0 {
			left := chAnsi.Cut(bgLine, 0, x)
			pos = chAnsi.StringWidth(left)
			b.WriteString(left)
			if pos < x {
				b.WriteString(ws.render(x - pos))
				pos = x
			}
		}

		fgLine := fgLines[i-y]
		b.WriteString(fgLine)
		pos += chAnsi.StringWidth(fgLine)

		right := cutLeft(bgLine, pos)
		bgWidth := chAnsi.StringWidth(bgLine)
		rightWidth := chAnsi.StringWidth(right)
		if rightWidth <= bgWidth-pos {
			b.WriteString(ws.render(bgWidth - rightWidth - pos))
		}

		b.WriteString(right)
	}

	return b.String()
}

// cutLeft cuts printable characters from the left.
// This function is heavily based on muesli's ansi and truncate packages.
func cutLeft(s string, cutWidth int) string {
	return chAnsi.Cut(s, cutWidth, chAnsi.StringWidth(s))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type whitespace struct {
	style termenv.Style
	chars string
}

// Render whitespaces.
func (w whitespace) render(width int) string {
	if width <= 0 {
		return ""
	}
	if w.chars == "" {
		w.chars = " "
	}

	patternWidth := chAnsi.StringWidth(w.chars)
	if patternWidth <= 0 {
		return w.style.Styled(strings.Repeat(" ", width))
	}

	// Repeat and then clip the whole pattern so grapheme clusters remain intact.
	// A final plain-space fill handles a wide glyph that cannot occupy the last
	// single cell.
	repeated := strings.Repeat(w.chars, width/patternWidth+1)
	content := chAnsi.Cut(repeated, 0, width)
	short := width - chAnsi.StringWidth(content)
	if short > 0 {
		content += strings.Repeat(" ", short)
	}

	return w.style.Styled(content)
}

// WhitespaceOption sets a styling rule for rendering whitespace.
type WhitespaceOption func(*whitespace)
