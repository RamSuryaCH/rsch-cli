package theme

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	// WCAG 2.x AA contrast for normal text. Terminal text should be treated as
	// normal text because font size and weight are controlled by the user.
	minimumTextContrast = 4.5
	// WCAG 2.x contrast for meaningful UI boundaries and focus indicators.
	minimumUIContrast = 3.0
)

// ensureAccessibleContrast preserves a theme's colors when they already meet
// contrast requirements and moves only failing colors toward black or white.
// Applying this once in each constructor keeps brand palettes intact as far as
// possible while making every built-in theme safe for ordinary terminal text.
func ensureAccessibleContrast(t *BaseTheme) {
	surfaces := []lipgloss.AdaptiveColor{
		t.BackgroundColor,
		t.BackgroundSecondaryColor,
		t.BackgroundDarkerColor,
	}

	textColors := []*lipgloss.AdaptiveColor{
		&t.PrimaryColor,
		&t.SecondaryColor,
		&t.AccentColor,
		&t.ErrorColor,
		&t.WarningColor,
		&t.SuccessColor,
		&t.InfoColor,
		&t.TextColor,
		&t.TextMutedColor,
		&t.TextEmphasizedColor,
		&t.MarkdownTextColor,
		&t.MarkdownHeadingColor,
		&t.MarkdownLinkColor,
		&t.MarkdownLinkTextColor,
		&t.MarkdownCodeColor,
		&t.MarkdownBlockQuoteColor,
		&t.MarkdownEmphColor,
		&t.MarkdownStrongColor,
		&t.MarkdownHorizontalRuleColor,
		&t.MarkdownListItemColor,
		&t.MarkdownListEnumerationColor,
		&t.MarkdownImageColor,
		&t.MarkdownImageTextColor,
		&t.MarkdownCodeBlockColor,
		&t.SyntaxCommentColor,
		&t.SyntaxKeywordColor,
		&t.SyntaxFunctionColor,
		&t.SyntaxVariableColor,
		&t.SyntaxStringColor,
		&t.SyntaxNumberColor,
		&t.SyntaxTypeColor,
		&t.SyntaxOperatorColor,
		&t.SyntaxPunctuationColor,
	}
	for _, color := range textColors {
		*color = ensureAdaptiveContrast(*color, surfaces, minimumTextContrast)
	}

	baseSurface := []lipgloss.AdaptiveColor{t.BackgroundColor}
	borderColors := []*lipgloss.AdaptiveColor{
		&t.BorderNormalColor,
		&t.BorderFocusedColor,
		&t.BorderDimColor,
	}
	for _, color := range borderColors {
		*color = ensureAdaptiveContrast(*color, baseSurface, minimumUIContrast)
	}

	// Diff foregrounds can be rendered either on the main surface or on any of
	// the diff surfaces, depending on unified/side-by-side presentation.
	diffSurfaces := []lipgloss.AdaptiveColor{
		t.BackgroundColor,
		t.DiffAddedBgColor,
		t.DiffRemovedBgColor,
		t.DiffContextBgColor,
		t.DiffAddedLineNumberBgColor,
		t.DiffRemovedLineNumberBgColor,
	}
	diffColors := []*lipgloss.AdaptiveColor{
		&t.DiffAddedColor,
		&t.DiffRemovedColor,
		&t.DiffContextColor,
		&t.DiffHunkHeaderColor,
		&t.DiffHighlightAddedColor,
		&t.DiffHighlightRemovedColor,
		&t.DiffLineNumberColor,
	}
	for _, color := range diffColors {
		*color = ensureAdaptiveContrast(*color, diffSurfaces, minimumTextContrast)
	}
}

func ensureAdaptiveContrast(foreground lipgloss.AdaptiveColor, backgrounds []lipgloss.AdaptiveColor, minimum float64) lipgloss.AdaptiveColor {
	darkBackgrounds := make([]string, 0, len(backgrounds))
	lightBackgrounds := make([]string, 0, len(backgrounds))
	for _, background := range backgrounds {
		darkBackgrounds = append(darkBackgrounds, background.Dark)
		lightBackgrounds = append(lightBackgrounds, background.Light)
	}
	return lipgloss.AdaptiveColor{
		Dark:  ensureContrast(foreground.Dark, darkBackgrounds, minimum),
		Light: ensureContrast(foreground.Light, lightBackgrounds, minimum),
	}
}

func ensureContrast(foreground string, backgrounds []string, minimum float64) string {
	fg, ok := parseHexColor(foreground)
	if !ok || len(backgrounds) == 0 {
		return foreground
	}

	parsedBackgrounds := make([]rgbColor, 0, len(backgrounds))
	for _, value := range backgrounds {
		background, valid := parseHexColor(value)
		if !valid {
			return foreground
		}
		parsedBackgrounds = append(parsedBackgrounds, background)
	}
	if minimumContrast(fg, parsedBackgrounds) >= minimum {
		return foreground
	}

	black := rgbColor{}
	white := rgbColor{r: 255, g: 255, b: 255}
	target := black
	if minimumContrast(white, parsedBackgrounds) > minimumContrast(black, parsedBackgrounds) {
		target = white
	}

	// Search in small deterministic increments so the result remains close to
	// the authored hue instead of replacing every failing token wholesale.
	for step := 1; step <= 1000; step++ {
		candidate := mixColor(fg, target, float64(step)/1000)
		if minimumContrast(candidate, parsedBackgrounds) >= minimum {
			return formatHexColor(candidate)
		}
	}
	return formatHexColor(target)
}

type rgbColor struct {
	r, g, b uint8
}

func parseHexColor(value string) (rgbColor, bool) {
	hex := strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(hex) == 3 {
		hex = strings.Repeat(hex[0:1], 2) + strings.Repeat(hex[1:2], 2) + strings.Repeat(hex[2:3], 2)
	}
	if len(hex) != 6 {
		return rgbColor{}, false
	}
	parsed, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return rgbColor{}, false
	}
	return rgbColor{r: uint8(parsed >> 16), g: uint8(parsed >> 8), b: uint8(parsed)}, true
}

func formatHexColor(color rgbColor) string {
	return fmt.Sprintf("#%02X%02X%02X", color.r, color.g, color.b)
}

func mixColor(from, to rgbColor, amount float64) rgbColor {
	mix := func(a, b uint8) uint8 {
		return uint8(math.Round(float64(a) + (float64(b)-float64(a))*amount))
	}
	return rgbColor{r: mix(from.r, to.r), g: mix(from.g, to.g), b: mix(from.b, to.b)}
}

func minimumContrast(foreground rgbColor, backgrounds []rgbColor) float64 {
	minimum := math.Inf(1)
	for _, background := range backgrounds {
		minimum = math.Min(minimum, contrastRatio(foreground, background))
	}
	return minimum
}

func contrastRatio(a, b rgbColor) float64 {
	lighter := math.Max(relativeLuminance(a), relativeLuminance(b))
	darker := math.Min(relativeLuminance(a), relativeLuminance(b))
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(color rgbColor) float64 {
	linear := func(value uint8) float64 {
		component := float64(value) / 255
		if component <= 0.04045 {
			return component / 12.92
		}
		return math.Pow((component+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(color.r) + 0.7152*linear(color.g) + 0.0722*linear(color.b)
}
