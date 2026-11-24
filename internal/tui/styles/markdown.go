package styles

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

const defaultMargin = 1

var renderedMarkdownSGR = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// Helper functions for style pointers
func boolPtr(b bool) *bool       { return &b }
func stringPtr(s string) *string { return &s }
func uintPtr(u uint) *uint       { return &u }

// MarkdownRenderer renders untrusted Markdown and contains the resulting ANSI
// output to the requested terminal width. Glamour wraps ordinary prose, but
// fenced code and very long tokens can otherwise remain wider than its word
// wrap setting and escape the surrounding viewport.
type MarkdownRenderer struct {
	renderer *glamour.TermRenderer
	width    int
	initErr  error
}

// GetMarkdownRenderer returns a Markdown renderer configured with the current
// theme and the requested terminal-cell width.
func GetMarkdownRenderer(width int) *MarkdownRenderer {
	width = max(1, width)
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(generateMarkdownStyleConfig()),
		glamour.WithWordWrap(width),
	)
	return &MarkdownRenderer{renderer: r, width: width, initErr: err}
}

// Render sanitizes input before parsing and performs a final cell-aware
// containment pass over Glamour's ANSI output. Tabs are made deterministic so
// their display width cannot depend on the user's terminal tab stops.
func (r *MarkdownRenderer) Render(content string) (string, error) {
	content = SanitizeTerminalText(content)
	if r.initErr != nil {
		// Callers historically fall back to sanitized source when Glamour cannot
		// render. Contain that fallback here so the failure path cannot escape the
		// viewport either.
		return containMarkdownWidth(content, r.width), nil
	}

	rendered, err := r.renderer.Render(content)
	if err != nil {
		return containMarkdownWidth(content, r.width), nil
	}
	return containMarkdownWidth(sanitizeRenderedMarkdown(rendered), r.width), nil
}

// Markdown parsing decodes HTML entities, which can introduce controls after
// source sanitization. Keep the renderer's SGR styling, and sanitize every
// other span before it reaches terminal layout or display.
func sanitizeRenderedMarkdown(rendered string) string {
	var clean strings.Builder
	clean.Grow(len(rendered))
	start := 0
	for _, match := range renderedMarkdownSGR.FindAllStringIndex(rendered, -1) {
		clean.WriteString(SanitizeTerminalText(rendered[start:match[0]]))
		clean.WriteString(rendered[match[0]:match[1]])
		start = match[1]
	}
	clean.WriteString(SanitizeTerminalText(rendered[start:]))
	return clean.String()
}

func containMarkdownWidth(rendered string, width int) string {
	if rendered == "" {
		return ""
	}

	// A literal tab can occupy a different number of cells depending on its
	// starting column and terminal configuration. Four spaces retain readable
	// source indentation while making containment deterministic.
	rendered = strings.ReplaceAll(rendered, "\t", "    ")
	originalLines := strings.Split(rendered, "\n")
	containedLines := make([]string, 0, len(originalLines))
	for _, line := range originalLines {
		if xansi.StringWidth(line) <= width {
			containedLines = append(containedLines, line)
			continue
		}

		wrapped := xansi.Hardwrap(line, width, true)
		for _, wrappedLine := range strings.Split(wrapped, "\n") {
			// A single grapheme may itself be wider than a very narrow viewport.
			// Truncate only that impossible-to-fit remainder while retaining ANSI
			// reset sequences emitted after the visible content.
			containedLines = append(containedLines, xansi.Truncate(wrappedLine, width, ""))
		}
	}
	return strings.Join(containedLines, "\n")
}

// creates an ansi.StyleConfig for markdown rendering
// using adaptive colors from the provided theme.
func generateMarkdownStyleConfig() ansi.StyleConfig {
	t := theme.CurrentTheme()

	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				BlockPrefix: "",
				BlockSuffix: "",
				Color:       stringPtr(adaptiveColorToString(t.MarkdownText())),
			},
			Margin: uintPtr(defaultMargin),
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:  stringPtr(adaptiveColorToString(t.MarkdownBlockQuote())),
				Italic: boolPtr(true),
				Prefix: Glyph("┃", "|") + " ",
			},
			Indent:      uintPtr(1),
			IndentToken: stringPtr(BaseStyle().Render(" ")),
		},
		List: ansi.StyleList{
			LevelIndent: defaultMargin,
			StyleBlock: ansi.StyleBlock{
				IndentToken: stringPtr(BaseStyle().Render(" ")),
				StylePrimitive: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.MarkdownText())),
				},
			},
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				BlockSuffix: "\n",
				Color:       stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:        boolPtr(true),
			},
		},
		H1: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "# ",
				Color:  stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:   boolPtr(true),
			},
		},
		H2: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "## ",
				Color:  stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:   boolPtr(true),
			},
		},
		H3: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "### ",
				Color:  stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:   boolPtr(true),
			},
		},
		H4: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "#### ",
				Color:  stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:   boolPtr(true),
			},
		},
		H5: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "##### ",
				Color:  stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:   boolPtr(true),
			},
		},
		H6: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: "###### ",
				Color:  stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				Bold:   boolPtr(true),
			},
		},
		Strikethrough: ansi.StylePrimitive{
			CrossedOut: boolPtr(true),
			Color:      stringPtr(adaptiveColorToString(t.TextMuted())),
		},
		Emph: ansi.StylePrimitive{
			Color:  stringPtr(adaptiveColorToString(t.MarkdownEmph())),
			Italic: boolPtr(true),
		},
		Strong: ansi.StylePrimitive{
			Bold:  boolPtr(true),
			Color: stringPtr(adaptiveColorToString(t.MarkdownStrong())),
		},
		HorizontalRule: ansi.StylePrimitive{
			Color:  stringPtr(adaptiveColorToString(t.MarkdownHorizontalRule())),
			Format: "\n" + strings.Repeat(Glyph("─", "-"), 41) + "\n",
		},
		Item: ansi.StylePrimitive{
			BlockPrefix: Glyph("•", "*") + " ",
			Color:       stringPtr(adaptiveColorToString(t.MarkdownListItem())),
		},
		Enumeration: ansi.StylePrimitive{
			BlockPrefix: ". ",
			Color:       stringPtr(adaptiveColorToString(t.MarkdownListEnumeration())),
		},
		Task: ansi.StyleTask{
			StylePrimitive: ansi.StylePrimitive{},
			Ticked:         "[" + Glyph("✓", "x") + "] ",
			Unticked:       "[ ] ",
		},
		Link: ansi.StylePrimitive{
			Color:     stringPtr(adaptiveColorToString(t.MarkdownLink())),
			Underline: boolPtr(true),
		},
		LinkText: ansi.StylePrimitive{
			Color: stringPtr(adaptiveColorToString(t.MarkdownLinkText())),
			Bold:  boolPtr(true),
		},
		Image: ansi.StylePrimitive{
			Color:     stringPtr(adaptiveColorToString(t.MarkdownImage())),
			Underline: boolPtr(true),
			Format:    Glyph("▣", "[image]") + " {{.text}}",
		},
		ImageText: ansi.StylePrimitive{
			Color:  stringPtr(adaptiveColorToString(t.MarkdownImageText())),
			Format: "{{.text}}",
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:  stringPtr(adaptiveColorToString(t.MarkdownCode())),
				Prefix: "",
				Suffix: "",
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Prefix: " ",
					Color:  stringPtr(adaptiveColorToString(t.MarkdownCodeBlock())),
				},
				Margin: uintPtr(defaultMargin),
			},
			Chroma: &ansi.Chroma{
				Text: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.MarkdownText())),
				},
				Error: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.Error())),
				},
				Comment: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxComment())),
				},
				CommentPreproc: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxKeyword())),
				},
				Keyword: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxKeyword())),
				},
				KeywordReserved: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxKeyword())),
				},
				KeywordNamespace: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxKeyword())),
				},
				KeywordType: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxType())),
				},
				Operator: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxOperator())),
				},
				Punctuation: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxPunctuation())),
				},
				Name: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxVariable())),
				},
				NameBuiltin: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxVariable())),
				},
				NameTag: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxKeyword())),
				},
				NameAttribute: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxFunction())),
				},
				NameClass: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxType())),
				},
				NameConstant: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxVariable())),
				},
				NameDecorator: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxFunction())),
				},
				NameFunction: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxFunction())),
				},
				LiteralNumber: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxNumber())),
				},
				LiteralString: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxString())),
				},
				LiteralStringEscape: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.SyntaxKeyword())),
				},
				GenericDeleted: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.DiffRemoved())),
				},
				GenericEmph: ansi.StylePrimitive{
					Color:  stringPtr(adaptiveColorToString(t.MarkdownEmph())),
					Italic: boolPtr(true),
				},
				GenericInserted: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.DiffAdded())),
				},
				GenericStrong: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.MarkdownStrong())),
					Bold:  boolPtr(true),
				},
				GenericSubheading: ansi.StylePrimitive{
					Color: stringPtr(adaptiveColorToString(t.MarkdownHeading())),
				},
			},
		},
		Table: ansi.StyleTable{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					BlockPrefix: "\n",
					BlockSuffix: "\n",
				},
			},
			CenterSeparator: stringPtr(Glyph("┼", "+")),
			ColumnSeparator: stringPtr(Glyph("│", "|")),
			RowSeparator:    stringPtr(Glyph("─", "-")),
		},
		DefinitionDescription: ansi.StylePrimitive{
			BlockPrefix: "\n " + Glyph("❯", ">") + " ",
			Color:       stringPtr(adaptiveColorToString(t.MarkdownLinkText())),
		},
		Text: ansi.StylePrimitive{
			Color: stringPtr(adaptiveColorToString(t.MarkdownText())),
		},
		Paragraph: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: stringPtr(adaptiveColorToString(t.MarkdownText())),
			},
		},
	}
}

// adaptiveColorToString converts a lipgloss.AdaptiveColor to the appropriate
// hex color string based on the current terminal background
func adaptiveColorToString(color lipgloss.AdaptiveColor) string {
	if lipgloss.HasDarkBackground() {
		return color.Dark
	}
	return color.Light
}
