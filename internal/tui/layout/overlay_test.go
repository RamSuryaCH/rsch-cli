package layout

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestPlaceOverlayPreservesNormalPlacement(t *testing.T) {
	background := "abcd\nefgh\nijkl"
	foreground := "XY\nZZ"

	if got, want := ansi.Strip(PlaceOverlay(1, 1, foreground, background, false)), "abcd\neXYh\niZZl"; got != want {
		t.Fatalf("PlaceOverlay() = %q, want %q", got, want)
	}
}

func TestPlaceOverlayOversizedForegroundIsClippedToBackground(t *testing.T) {
	tests := []struct {
		name       string
		x, y       int
		foreground string
		background string
		shadow     bool
	}{
		{name: "width", x: 2, foreground: "0123456789", background: "abcd\nefgh"},
		{name: "height", x: 2, y: 1, foreground: "X\nY\nZ", background: "abcd\nefgh"},
		{name: "both", x: -10, y: 20, foreground: "012345\nabcdef\nUVWXYZ", background: "....\n...."},
		{name: "tiny wide glyph", foreground: "界", background: "x"},
		{name: "oversized shadow", x: 20, y: 20, foreground: "012345\nabcdef\nUVWXYZ", background: "....\n....", shadow: true},
		{name: "zero width", foreground: "oversized", background: "\n"},
		{name: "empty", foreground: "oversized", background: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlaceOverlay(tc.x, tc.y, tc.foreground, tc.background, tc.shadow)
			assertOverlayWithinBackground(t, got, tc.background)
		})
	}
}

func TestPlaceOverlayClipsANSIAndUnicodeAtCellBoundaries(t *testing.T) {
	background := ".....\n....."
	foreground := "\x1b[31m界e\u0301👩‍💻Z\x1b[0m"

	got := PlaceOverlay(3, 0, foreground, background, false)
	assertOverlayWithinBackground(t, got, background)
	if !utf8.ValidString(got) {
		t.Fatalf("PlaceOverlay returned invalid UTF-8: %q", got)
	}
	if first := strings.Split(ansi.Strip(got), "\n")[0]; first != "界e\u0301👩‍💻" {
		t.Fatalf("clipped Unicode line = %q, want complete grapheme clusters", first)
	}
}

func TestPlaceOverlayRaggedBackgroundRetainsFrameHeight(t *testing.T) {
	background := "abcdef\nx\n"
	got := PlaceOverlay(4, 1, "OVERFLOW\nSECOND\nTHIRD", background, false)

	assertOverlayWithinBackground(t, got, background)
	if gotHeight, wantHeight := len(strings.Split(got, "\n")), len(strings.Split(background, "\n")); gotHeight != wantHeight {
		t.Fatalf("overlay height = %d, want %d", gotHeight, wantHeight)
	}
}

func TestWhitespaceRenderNeverExceedsRequestedWidth(t *testing.T) {
	for _, pattern := range []string{"界.", ".界", "e\u0301", "👩‍💻."} {
		for width := 0; width <= 8; width++ {
			got := (whitespace{chars: pattern}).render(width)
			if gotWidth := ansi.StringWidth(got); gotWidth != width {
				t.Fatalf("pattern %q at width %d rendered width %d: %q", pattern, width, gotWidth, got)
			}
		}
	}
}

func FuzzPlaceOverlayStaysWithinBackground(f *testing.F) {
	for _, seed := range []struct {
		foreground string
		width      uint8
		height     uint8
		x          int16
		y          int16
	}{
		{foreground: "plain", width: 1, height: 1},
		{foreground: "界e\u0301👩‍💻", width: 5, height: 2, x: -2, y: 9},
		{foreground: "\x1b[31mred\x1b[0m\nsecond", width: 8, height: 3, x: 7, y: -4},
	} {
		f.Add(seed.foreground, seed.width, seed.height, seed.x, seed.y)
	}

	f.Fuzz(func(t *testing.T, foreground string, rawWidth, rawHeight uint8, x, y int16) {
		if !utf8.ValidString(foreground) {
			t.Skip()
		}
		width := int(rawWidth%64) + 1
		height := int(rawHeight%32) + 1
		line := strings.Repeat(".", width)
		background := strings.Repeat(line+"\n", height-1) + line

		got := PlaceOverlay(int(x), int(y), foreground, background, false)
		assertOverlayWithinBackground(t, got, background)
		if !utf8.ValidString(got) {
			t.Fatalf("PlaceOverlay returned invalid UTF-8: %q", got)
		}
	})
}

func assertOverlayWithinBackground(t *testing.T, got, background string) {
	t.Helper()
	backgroundLines, backgroundWidth := getLines(background)
	gotLines := strings.Split(got, "\n")
	if len(gotLines) != len(backgroundLines) {
		t.Fatalf("overlay height %d changed background height %d: %q", len(gotLines), len(backgroundLines), got)
	}
	for i, line := range gotLines {
		if width := lipgloss.Width(line); width > backgroundWidth {
			t.Fatalf("overlay line %d width %d exceeds background width %d: %q", i, width, backgroundWidth, line)
		}
	}
}
