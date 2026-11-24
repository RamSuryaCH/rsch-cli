package layout

// Responsive metrics are shared by pages and components so that resizing the
// terminal produces the same decisions everywhere. The values intentionally
// use conservative thresholds: the conversation and composer stay usable
// before optional context is allowed to consume space.

const (
	CompactWidth = 80
	SidebarWidth = 148
	LargeWidth   = 160

	contentGutter   = 4
	maxContentWidth = 108
)

// SidebarVisible reports whether a secondary context panel has enough room to
// coexist with the conversation. At smaller sizes the primary workflow wins.
func SidebarVisible(width int) bool {
	return width >= SidebarWidth
}

// ComposerHeight returns a small, stable composer allocation for the terminal
// height. It prevents large terminals from turning the input into dead space
// while retaining enough room for pasted or multiline prompts.
func ComposerHeight(height int) int {
	switch {
	case height <= 0:
		return 0
	case height <= 3:
		return height
	case height <= 12:
		return 3
	case height <= 24:
		return 4
	case height <= 40:
		return 5
	default:
		return 6
	}
}

// ContentWidth keeps markdown and tool output inside readable bounds while
// still using the available terminal real estate.
func ContentWidth(width int) int {
	if width <= 0 {
		return 0
	}

	// Introduce the four-column reading gutter without making the content
	// narrower when the terminal grows from 79 to 80 columns. Compact terminals
	// use every cell until the future gutter would begin; the content then holds
	// steady while the gutter is introduced and resumes growing above the
	// compact breakpoint.
	compactContentWidth := CompactWidth - contentGutter
	if width < CompactWidth {
		return min(width, compactContentWidth)
	}

	return min(width-contentGutter, maxContentWidth)
}
