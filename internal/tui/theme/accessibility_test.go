package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBuiltInThemesMeetTextContrast(t *testing.T) {
	themes := map[string]Theme{
		"catppuccin": NewCatppuccinTheme(),
		"dracula":    NewDraculaTheme(),
		"flexoki":    NewFlexokiTheme(),
		"gruvbox":    NewGruvboxTheme(),
		"monokai":    NewMonokaiProTheme(),
		"onedark":    NewOneDarkTheme(),
		"rsch":       NewOpenCodeTheme(),
		"tokyonight": NewTokyoNightTheme(),
		"tron":       NewTronTheme(),
	}

	for name, theme := range themes {
		t.Run(name, func(t *testing.T) {
			surfaces := []lipgloss.AdaptiveColor{
				theme.Background(),
				theme.BackgroundSecondary(),
				theme.BackgroundDarker(),
			}
			textColors := map[string]lipgloss.AdaptiveColor{
				"primary":              theme.Primary(),
				"secondary":            theme.Secondary(),
				"accent":               theme.Accent(),
				"error":                theme.Error(),
				"warning":              theme.Warning(),
				"success":              theme.Success(),
				"info":                 theme.Info(),
				"text":                 theme.Text(),
				"text-muted":           theme.TextMuted(),
				"text-emphasized":      theme.TextEmphasized(),
				"markdown-text":        theme.MarkdownText(),
				"markdown-heading":     theme.MarkdownHeading(),
				"markdown-link":        theme.MarkdownLink(),
				"markdown-link-text":   theme.MarkdownLinkText(),
				"markdown-code":        theme.MarkdownCode(),
				"markdown-blockquote":  theme.MarkdownBlockQuote(),
				"markdown-emphasis":    theme.MarkdownEmph(),
				"markdown-strong":      theme.MarkdownStrong(),
				"markdown-rule":        theme.MarkdownHorizontalRule(),
				"markdown-list":        theme.MarkdownListItem(),
				"markdown-enumeration": theme.MarkdownListEnumeration(),
				"markdown-image":       theme.MarkdownImage(),
				"markdown-image-text":  theme.MarkdownImageText(),
				"markdown-code-block":  theme.MarkdownCodeBlock(),
				"syntax-comment":       theme.SyntaxComment(),
				"syntax-keyword":       theme.SyntaxKeyword(),
				"syntax-function":      theme.SyntaxFunction(),
				"syntax-variable":      theme.SyntaxVariable(),
				"syntax-string":        theme.SyntaxString(),
				"syntax-number":        theme.SyntaxNumber(),
				"syntax-type":          theme.SyntaxType(),
				"syntax-operator":      theme.SyntaxOperator(),
				"syntax-punctuation":   theme.SyntaxPunctuation(),
			}
			for token, foreground := range textColors {
				assertAdaptiveContrast(t, token, foreground, surfaces, minimumTextContrast)
			}

			for token, foreground := range map[string]lipgloss.AdaptiveColor{
				"border":         theme.BorderNormal(),
				"border-focused": theme.BorderFocused(),
				"border-dim":     theme.BorderDim(),
			} {
				assertAdaptiveContrast(t, token, foreground, []lipgloss.AdaptiveColor{theme.Background()}, minimumUIContrast)
			}

			diffSurfaces := []lipgloss.AdaptiveColor{
				theme.Background(),
				theme.DiffAddedBg(),
				theme.DiffRemovedBg(),
				theme.DiffContextBg(),
				theme.DiffAddedLineNumberBg(),
				theme.DiffRemovedLineNumberBg(),
			}
			for token, foreground := range map[string]lipgloss.AdaptiveColor{
				"diff-added":             theme.DiffAdded(),
				"diff-removed":           theme.DiffRemoved(),
				"diff-context":           theme.DiffContext(),
				"diff-hunk-header":       theme.DiffHunkHeader(),
				"diff-highlight-added":   theme.DiffHighlightAdded(),
				"diff-highlight-removed": theme.DiffHighlightRemoved(),
				"diff-line-number":       theme.DiffLineNumber(),
			} {
				assertAdaptiveContrast(t, token, foreground, diffSurfaces, minimumTextContrast)
			}
		})
	}
}

func TestEnsureContrastPreservesCompliantColor(t *testing.T) {
	const foreground = "#e0e0e0"
	if got := ensureContrast(foreground, []string{"#212121"}, minimumTextContrast); got != foreground {
		t.Fatalf("ensureContrast() changed compliant color: got %q want %q", got, foreground)
	}
}

func assertAdaptiveContrast(t *testing.T, token string, foreground lipgloss.AdaptiveColor, backgrounds []lipgloss.AdaptiveColor, minimum float64) {
	t.Helper()
	for _, variant := range []struct {
		name       string
		foreground string
		background func(lipgloss.AdaptiveColor) string
	}{
		{name: "dark", foreground: foreground.Dark, background: func(color lipgloss.AdaptiveColor) string { return color.Dark }},
		{name: "light", foreground: foreground.Light, background: func(color lipgloss.AdaptiveColor) string { return color.Light }},
	} {
		fg, ok := parseHexColor(variant.foreground)
		if !ok {
			t.Fatalf("%s %s foreground %q is not a hex color", token, variant.name, variant.foreground)
		}
		for _, adaptiveBackground := range backgrounds {
			backgroundValue := variant.background(adaptiveBackground)
			background, valid := parseHexColor(backgroundValue)
			if !valid {
				t.Fatalf("%s %s background %q is not a hex color", token, variant.name, backgroundValue)
			}
			if ratio := contrastRatio(fg, background); ratio+0.0001 < minimum {
				t.Errorf("%s %s contrast %.2f:1 (%s on %s), want at least %.1f:1", token, variant.name, ratio, variant.foreground, backgroundValue, minimum)
			}
		}
	}
}
