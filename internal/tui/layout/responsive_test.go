package layout

import (
	"fmt"
	"testing"
)

func TestResponsiveMetrics(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		sidebar       bool
		composer      int
	}{
		{80, 24, false, 4},
		{100, 30, false, 5},
		{120, 40, false, 5},
		{148, 40, true, 5},
		{160, 50, true, 6},
	} {
		if got := SidebarVisible(tc.width); got != tc.sidebar {
			t.Fatalf("SidebarVisible(%d) = %t, want %t", tc.width, got, tc.sidebar)
		}
		if got := ComposerHeight(tc.height); got != tc.composer {
			t.Fatalf("ComposerHeight(%d) = %d, want %d", tc.height, got, tc.composer)
		}
	}
}

func TestResponsiveWidthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		width   int
		content int
		sidebar bool
	}{
		{width: 1, content: 1, sidebar: false},
		{width: 40, content: 40, sidebar: false},
		{width: 79, content: 76, sidebar: false},
		{width: 80, content: 76, sidebar: false},
		{width: 112, content: 108, sidebar: false},
		{width: 119, content: 108, sidebar: false},
		{width: 120, content: 108, sidebar: false},
		{width: 147, content: 108, sidebar: false},
		{width: 148, content: 108, sidebar: true},
		{width: 160, content: 108, sidebar: true},
		{width: 240, content: 108, sidebar: true},
	} {
		t.Run(fmt.Sprintf("width_%d", tc.width), func(t *testing.T) {
			if got := ContentWidth(tc.width); got != tc.content {
				t.Fatalf("ContentWidth(%d) = %d, want %d", tc.width, got, tc.content)
			}
			if got := SidebarVisible(tc.width); got != tc.sidebar {
				t.Fatalf("SidebarVisible(%d) = %t, want %t", tc.width, got, tc.sidebar)
			}
		})
	}
}

func TestContentWidthIsMonotonicAndBounded(t *testing.T) {
	previous := ContentWidth(0)
	for width := 0; width <= 240; width++ {
		got := ContentWidth(width)
		if got < 0 {
			t.Fatalf("ContentWidth(%d) = %d, want non-negative", width, got)
		}
		if got > width {
			t.Fatalf("ContentWidth(%d) = %d, exceeds terminal", width, got)
		}
		if got > maxContentWidth {
			t.Fatalf("ContentWidth(%d) = %d, exceeds readable maximum %d", width, got, maxContentWidth)
		}
		if got < previous {
			t.Fatalf("ContentWidth decreased from %d at width %d to %d at width %d", previous, width-1, got, width)
		}
		previous = got
	}
}

func TestComposerHeightFitsTinyTerminals(t *testing.T) {
	for height := 0; height <= 3; height++ {
		if got := ComposerHeight(height); got != height {
			t.Fatalf("ComposerHeight(%d) = %d, want %d", height, got, height)
		}
	}
}
