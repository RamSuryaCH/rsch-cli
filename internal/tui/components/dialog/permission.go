package dialog

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type PermissionAction string

// Permission responses
const (
	PermissionAllow           PermissionAction = "allow"
	PermissionAllowForSession PermissionAction = "allow_session"
	PermissionDeny            PermissionAction = "deny"
)

// PermissionResponseMsg represents the user's response to a permission request
type PermissionResponseMsg struct {
	Permission permission.PermissionRequest
	Action     PermissionAction
}

// PermissionDialogCmp interface for permission dialog component
type PermissionDialogCmp interface {
	tea.Model
	layout.Bindings
	SetPermissions(permission permission.PermissionRequest) tea.Cmd
}

type permissionsMapping struct {
	Left   key.Binding
	Right  key.Binding
	Enter  key.Binding
	Escape key.Binding
	Tab    key.Binding
}

type permissionOption int

const (
	// Keep Deny as the zero value so an incompletely initialized dialog still
	// fails closed when Enter is pressed.
	permissionOptionDeny permissionOption = iota
	permissionOptionAllow
	permissionOptionAllowForSession
)

var permissionsKeys = permissionsMapping{
	Left: key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "switch options"),
	),
	Right: key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "switch options"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "deny"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch options"),
	),
}

// permissionDialogCmp is the implementation of PermissionDialog
type permissionDialogCmp struct {
	width           int
	height          int
	permission      permission.PermissionRequest
	windowSize      tea.WindowSizeMsg
	contentViewPort viewport.Model
	selectedOption  permissionOption

	diffCache     map[string]string
	markdownCache map[string]string
}

func (p *permissionDialogCmp) Init() tea.Cmd {
	return p.contentViewPort.Init()
}

func (p *permissionDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.windowSize = msg
		cmd := p.SetSize()
		cmds = append(cmds, cmd)
		p.markdownCache = make(map[string]string)
		p.diffCache = make(map[string]string)
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, permissionsKeys.Right) || key.Matches(msg, permissionsKeys.Tab):
			switch p.selectedOption {
			case permissionOptionDeny:
				p.selectedOption = permissionOptionAllow
			case permissionOptionAllow:
				p.selectedOption = permissionOptionAllowForSession
			default:
				p.selectedOption = permissionOptionDeny
			}
			return p, nil
		case key.Matches(msg, permissionsKeys.Left):
			switch p.selectedOption {
			case permissionOptionDeny:
				p.selectedOption = permissionOptionAllowForSession
			case permissionOptionAllowForSession:
				p.selectedOption = permissionOptionAllow
			default:
				p.selectedOption = permissionOptionDeny
			}
		case key.Matches(msg, permissionsKeys.Enter):
			return p, p.selectCurrentOption()
		case key.Matches(msg, permissionsKeys.Escape):
			return p, util.CmdHandler(PermissionResponseMsg{Action: PermissionDeny, Permission: p.permission})
		default:
			// Pass other keys to viewport
			viewPort, cmd := p.contentViewPort.Update(msg)
			p.contentViewPort = viewPort
			cmds = append(cmds, cmd)
		}
	}

	return p, tea.Batch(cmds...)
}

func (p *permissionDialogCmp) selectCurrentOption() tea.Cmd {
	action := PermissionDeny

	switch p.selectedOption {
	case permissionOptionAllow:
		if p.hasValidDetails() {
			action = PermissionAllow
		}
	case permissionOptionAllowForSession:
		if p.hasValidDetails() {
			action = PermissionAllowForSession
		}
	}

	return util.CmdHandler(PermissionResponseMsg{Action: action, Permission: p.permission})
}

func (p *permissionDialogCmp) hasValidDetails() bool {
	switch p.permission.ToolName {
	case tools.BashToolName:
		_, ok := p.permission.Params.(tools.BashPermissionsParams)
		return ok
	case tools.EditToolName, tools.PatchToolName:
		_, ok := p.permission.Params.(tools.EditPermissionsParams)
		return ok
	case tools.WriteToolName:
		_, ok := p.permission.Params.(tools.WritePermissionsParams)
		return ok
	case tools.FetchToolName:
		_, ok := p.permission.Params.(tools.FetchPermissionsParams)
		return ok
	default:
		return true
	}
}

func (p *permissionDialogCmp) renderButtons() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	allowStyle := baseStyle
	allowSessionStyle := baseStyle
	denyStyle := baseStyle
	spacerStyle := baseStyle.Background(t.Background())

	// Style the selected button
	switch p.selectedOption {
	case permissionOptionAllow:
		allowStyle = allowStyle.Background(t.Primary()).Foreground(t.Background())
		allowSessionStyle = allowSessionStyle.Background(t.Background()).Foreground(t.Primary())
		denyStyle = denyStyle.Background(t.Background()).Foreground(t.Primary())
	case permissionOptionAllowForSession:
		allowStyle = allowStyle.Background(t.Background()).Foreground(t.Primary())
		allowSessionStyle = allowSessionStyle.Background(t.Primary()).Foreground(t.Background())
		denyStyle = denyStyle.Background(t.Background()).Foreground(t.Primary())
	default:
		allowStyle = allowStyle.Background(t.Background()).Foreground(t.Primary())
		allowSessionStyle = allowSessionStyle.Background(t.Background()).Foreground(t.Primary())
		denyStyle = denyStyle.Background(t.Primary()).Foreground(t.Background())
	}

	buttonLabel := func(option permissionOption, label string) string {
		if p.selectedOption == option {
			return "> " + label
		}
		return "  " + label
	}
	allowButton := allowStyle.Padding(0, 1).Render(buttonLabel(permissionOptionAllow, "Allow once"))
	allowSessionButton := allowSessionStyle.Padding(0, 1).Render(buttonLabel(permissionOptionAllowForSession, "Allow this session"))
	denyButton := denyStyle.Padding(0, 1).Render(buttonLabel(permissionOptionDeny, "Deny"))

	content := ""
	if p.width < 64 {
		content = lipgloss.JoinVertical(lipgloss.Left, denyButton, allowButton, allowSessionButton)
	} else {
		content = lipgloss.JoinHorizontal(
			lipgloss.Left,
			allowButton,
			spacerStyle.Render("  "),
			allowSessionButton,
			spacerStyle.Render("  "),
			denyButton,
		)
	}

	remainingWidth := p.width - lipgloss.Width(content)
	if remainingWidth > 0 {
		content = spacerStyle.Render(strings.Repeat(" ", remainingWidth)) + content
	}
	return content
}

func (p *permissionDialogCmp) renderHeader() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	toolKey := baseStyle.Foreground(t.TextMuted()).Bold(true).Render("Tool")
	toolValue := baseStyle.
		Foreground(t.Text()).
		Width(max(1, p.width-lipgloss.Width(toolKey))).
		Render(fmt.Sprintf(": %s", styles.SanitizeTerminalText(p.permission.ToolName)))

	pathKey := baseStyle.Foreground(t.TextMuted()).Bold(true).Render("Path")
	pathValue := baseStyle.
		Foreground(t.Text()).
		Width(max(1, p.width-lipgloss.Width(pathKey))).
		Render(fmt.Sprintf(": %s", styles.SanitizeTerminalText(p.permission.Path)))

	actionKey := baseStyle.Foreground(t.TextMuted()).Bold(true).Render("Action")
	actionValue := baseStyle.
		Foreground(t.Text()).
		Width(max(1, p.width-lipgloss.Width(actionKey))).
		Render(fmt.Sprintf(": %s · %s scope", styles.SanitizeTerminalText(p.permission.Action), p.permission.ScopeKind))

	headerParts := []string{
		lipgloss.JoinHorizontal(
			lipgloss.Left,
			toolKey,
			toolValue,
		),
		lipgloss.JoinHorizontal(
			lipgloss.Left,
			pathKey,
			pathValue,
		),
		lipgloss.JoinHorizontal(lipgloss.Left, actionKey, actionValue),
	}

	// Add tool-specific header information
	switch p.permission.ToolName {
	case tools.BashToolName:
		headerParts = append(headerParts, baseStyle.Foreground(t.TextMuted()).Width(p.width).Bold(true).Render("Command"))
	case tools.EditToolName, tools.PatchToolName:
		params, ok := p.permission.Params.(tools.EditPermissionsParams)
		if !ok {
			headerParts = append(headerParts, baseStyle.Foreground(t.Error()).Render("Invalid permission details — approval disabled"))
			break
		}
		fileKey := baseStyle.Foreground(t.TextMuted()).Bold(true).Render("File")
		filePath := baseStyle.
			Foreground(t.Text()).
			Width(max(1, p.width-lipgloss.Width(fileKey))).
			Render(fmt.Sprintf(": %s", styles.SanitizeTerminalText(params.FilePath)))
		headerParts = append(headerParts,
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				fileKey,
				filePath,
			),
		)

	case tools.WriteToolName:
		params, ok := p.permission.Params.(tools.WritePermissionsParams)
		if !ok {
			headerParts = append(headerParts, baseStyle.Foreground(t.Error()).Render("Invalid permission details — approval disabled"))
			break
		}
		fileKey := baseStyle.Foreground(t.TextMuted()).Bold(true).Render("File")
		filePath := baseStyle.
			Foreground(t.Text()).
			Width(max(1, p.width-lipgloss.Width(fileKey))).
			Render(fmt.Sprintf(": %s", styles.SanitizeTerminalText(params.FilePath)))
		headerParts = append(headerParts,
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				fileKey,
				filePath,
			),
		)
	case tools.FetchToolName:
		headerParts = append(headerParts, baseStyle.Foreground(t.TextMuted()).Width(p.width).Bold(true).Render("URL"))
	}

	return lipgloss.NewStyle().Background(t.Background()).Render(lipgloss.JoinVertical(lipgloss.Left, headerParts...))
}

func (p *permissionDialogCmp) renderBashContent() string {
	baseStyle := styles.BaseStyle()

	if pr, ok := p.permission.Params.(tools.BashPermissionsParams); ok {
		renderedContent := styles.SanitizeTerminalText(pr.Command)

		finalContent := baseStyle.
			Width(p.contentViewPort.Width).
			Render(renderedContent)
		p.contentViewPort.SetContent(finalContent)
		return p.styleViewport()
	}
	return ""
}

func (p *permissionDialogCmp) renderEditContent() string {
	if pr, ok := p.permission.Params.(tools.EditPermissionsParams); ok {
		diff := p.GetOrSetDiff(p.permission.ID, func() (string, error) {
			return diff.FormatDiff(styles.SanitizeTerminalText(pr.Diff), diff.WithTotalWidth(p.contentViewPort.Width))
		})

		p.contentViewPort.SetContent(diff)
		return p.styleViewport()
	}
	return ""
}

func (p *permissionDialogCmp) renderPatchContent() string {
	if pr, ok := p.permission.Params.(tools.EditPermissionsParams); ok {
		diff := p.GetOrSetDiff(p.permission.ID, func() (string, error) {
			return diff.FormatDiff(styles.SanitizeTerminalText(pr.Diff), diff.WithTotalWidth(p.contentViewPort.Width))
		})

		p.contentViewPort.SetContent(diff)
		return p.styleViewport()
	}
	return ""
}

func (p *permissionDialogCmp) renderWriteContent() string {
	if pr, ok := p.permission.Params.(tools.WritePermissionsParams); ok {
		// Use the cache for diff rendering
		diff := p.GetOrSetDiff(p.permission.ID, func() (string, error) {
			return diff.FormatDiff(styles.SanitizeTerminalText(pr.Diff), diff.WithTotalWidth(p.contentViewPort.Width))
		})

		p.contentViewPort.SetContent(diff)
		return p.styleViewport()
	}
	return ""
}

func (p *permissionDialogCmp) renderFetchContent() string {
	baseStyle := styles.BaseStyle()

	if pr, ok := p.permission.Params.(tools.FetchPermissionsParams); ok {
		renderedContent := styles.SanitizeTerminalText(pr.URL)

		finalContent := baseStyle.
			Width(p.contentViewPort.Width).
			Render(renderedContent)
		p.contentViewPort.SetContent(finalContent)
		return p.styleViewport()
	}
	return ""
}

func (p *permissionDialogCmp) renderDefaultContent() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	content := styles.SanitizeTerminalText(p.permission.Description)

	// Use the cache for markdown rendering
	renderedContent := p.GetOrSetMarkdown(p.permission.ID, func() (string, error) {
		r := styles.GetMarkdownRenderer(max(1, p.width-10))
		s, err := r.Render(content)
		return styles.ForceReplaceBackgroundWithLipgloss(s, t.Background()), err
	})

	finalContent := baseStyle.
		Width(p.contentViewPort.Width).
		Render(renderedContent)
	p.contentViewPort.SetContent(finalContent)

	if renderedContent == "" {
		return ""
	}

	return p.styleViewport()
}

func (p *permissionDialogCmp) styleViewport() string {
	t := theme.CurrentTheme()
	contentStyle := lipgloss.NewStyle().
		Background(t.Background())

	return contentStyle.Render(p.contentViewPort.View())
}

func (p *permissionDialogCmp) renderReviewHint() string {
	t := theme.CurrentTheme()
	total := p.contentViewPort.TotalLineCount()
	start, end := 0, 0
	if total > 0 {
		start = min(p.contentViewPort.YOffset+1, total)
		end = min(p.contentViewPort.YOffset+p.contentViewPort.VisibleLineCount(), total)
	}
	position := fmt.Sprintf("%d-%d/%d", start, end, total)
	return styles.BaseStyle().
		Foreground(t.TextMuted()).
		Width(max(1, p.width-4)).
		Render("Review: ↑/↓ scroll · space/pgup/pgdn page · " + position)
}

func (p *permissionDialogCmp) renderSessionTrustNotice() string {
	t := theme.CurrentTheme()
	tool := styles.SanitizeTerminalText(p.permission.ToolName)
	action := styles.SanitizeTerminalText(p.permission.Action)
	scope := styles.SanitizeTerminalText(string(p.permission.ScopeKind))
	path := styles.SanitizeTerminalText(p.permission.Path)
	return styles.BaseStyle().
		Foreground(t.Warning()).
		Width(max(1, p.width-4)).
		Render(fmt.Sprintf("Session grant trusts tool %q to %q in %s scope %q until this session ends.", tool, action, scope, path))
}

func (p *permissionDialogCmp) render() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	title := baseStyle.
		Bold(true).
		Width(max(1, p.width-4)).
		Foreground(t.Primary()).
		Render("Permission Required")
	// Render header
	headerContent := p.renderHeader()
	// Render buttons
	buttons := p.renderButtons()
	decisionHint := baseStyle.Foreground(t.TextMuted()).Width(max(1, p.width-4)).Render("←/→ select · enter confirm · esc deny")
	sessionTrustNotice := p.renderSessionTrustNotice()

	// Calculate content height dynamically based on window size
	reviewHintHeight := 1
	p.contentViewPort.Height = max(1, p.height-lipgloss.Height(headerContent)-lipgloss.Height(buttons)-lipgloss.Height(decisionHint)-lipgloss.Height(sessionTrustNotice)-reviewHintHeight-2-lipgloss.Height(title))
	p.contentViewPort.Width = max(1, p.width-4)

	// Render content based on tool type
	var contentFinal string
	switch p.permission.ToolName {
	case tools.BashToolName:
		contentFinal = p.renderBashContent()
	case tools.EditToolName:
		contentFinal = p.renderEditContent()
	case tools.PatchToolName:
		contentFinal = p.renderPatchContent()
	case tools.WriteToolName:
		contentFinal = p.renderWriteContent()
	case tools.FetchToolName:
		contentFinal = p.renderFetchContent()
	default:
		contentFinal = p.renderDefaultContent()
	}
	reviewHint := p.renderReviewHint()

	content := lipgloss.JoinVertical(
		lipgloss.Top,
		title,
		baseStyle.Render(strings.Repeat(" ", lipgloss.Width(title))),
		headerContent,
		contentFinal,
		reviewHint,
		decisionHint,
		sessionTrustNotice,
		buttons,
		baseStyle.Render(strings.Repeat(" ", max(0, p.width-4))),
	)

	return baseStyle.
		Padding(1, 0, 0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(p.width).
		Height(p.height).
		Render(
			content,
		)
}

func (p *permissionDialogCmp) View() string {
	return p.render()
}

func (p *permissionDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(permissionsKeys)
}

func (p *permissionDialogCmp) SetSize() tea.Cmd {
	if p.permission.ID == "" {
		return nil
	}
	windowWidth := p.windowSize.Width
	windowHeight := p.windowSize.Height
	if windowWidth <= 0 {
		windowWidth = 80
	}
	if windowHeight <= 0 {
		windowHeight = 24
	}
	availableWidth := max(1, windowWidth-4)
	availableHeight := max(1, windowHeight-2)
	targetWidth, targetHeight := 84, 18
	switch p.permission.ToolName {
	case tools.EditToolName, tools.PatchToolName, tools.WriteToolName:
		targetWidth = 110
		targetHeight = max(18, int(float64(windowHeight)*0.8))
	case tools.BashToolName, tools.FetchToolName:
		targetWidth = 76
		targetHeight = 18
	default:
		targetHeight = max(16, int(float64(windowHeight)*0.6))
	}
	p.width = min(targetWidth, availableWidth)
	p.height = min(targetHeight, availableHeight)
	return nil
}

func (p *permissionDialogCmp) SetPermissions(permission permission.PermissionRequest) tea.Cmd {
	p.permission = permission
	// Every request starts from the safe option. The dialog is reused, so
	// retaining the previous selection could otherwise make a later Enter key
	// approve a different request unintentionally.
	p.selectedOption = permissionOptionDeny
	p.contentViewPort.GotoTop()
	p.markdownCache = make(map[string]string)
	p.diffCache = make(map[string]string)
	return p.SetSize()
}

// Helper to get or set cached diff content
func (c *permissionDialogCmp) GetOrSetDiff(key string, generator func() (string, error)) string {
	if cached, ok := c.diffCache[key]; ok {
		return cached
	}

	content, err := generator()
	if err != nil {
		return fmt.Sprintf("Error formatting diff: %v", err)
	}

	c.diffCache[key] = content

	return content
}

// Helper to get or set cached markdown content
func (c *permissionDialogCmp) GetOrSetMarkdown(key string, generator func() (string, error)) string {
	if cached, ok := c.markdownCache[key]; ok {
		return cached
	}

	content, err := generator()
	if err != nil {
		return fmt.Sprintf("Error rendering markdown: %v", err)
	}

	c.markdownCache[key] = content

	return content
}

func NewPermissionDialogCmp() PermissionDialogCmp {
	// Create viewport for content
	contentViewport := viewport.New(0, 0)

	return &permissionDialogCmp{
		contentViewPort: contentViewport,
		selectedOption:  permissionOptionDeny,
		diffCache:       make(map[string]string),
		markdownCache:   make(map[string]string),
	}
}
