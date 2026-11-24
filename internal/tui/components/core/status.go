package core

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type StatusCmp interface {
	tea.Model
}

type statusCmp struct {
	info       util.InfoMsg
	width      int
	messageTTL time.Duration
	lspClients lsp.ClientProvider
	session    session.Session
	generation uint64
}

// clearMessageCmd is a command that clears status messages after a timeout
func (m statusCmp) clearMessageCmd(ttl time.Duration, generation uint64) tea.Cmd {
	return tea.Tick(ttl, func(time.Time) tea.Msg {
		return util.ClearStatusMsg{Generation: generation}
	})
}

func (m statusCmp) Init() tea.Cmd {
	return nil
}

func (m statusCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case chat.SessionSelectedMsg:
		m.session = msg
	case chat.SessionClearedMsg:
		m.session = session.Session{}
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent {
			if m.session.ID == msg.Payload.ID {
				m.session = msg.Payload
			}
		}
	case util.InfoMsg:
		m.info = msg
		m.generation++
		ttl := msg.TTL
		if ttl == 0 {
			ttl = m.messageTTL
		}
		return m, m.clearMessageCmd(ttl, m.generation)
	case util.ClearStatusMsg:
		if msg.Generation == m.generation {
			m.info = util.InfoMsg{}
		}
	}
	return m, nil
}

var helpWidget = ""

// getHelpWidget returns the help widget with current theme colors
func getHelpWidget() string {
	t := theme.CurrentTheme()
	helpText := "f1 help"

	return styles.Padded().
		Background(t.TextMuted()).
		Foreground(t.BackgroundDarker()).
		Bold(true).
		Render(helpText)
}

func formatTokensAndCost(tokens int64, cost float64) string {
	// Format tokens in human-readable format (e.g., 110K, 1.2M)
	var formattedTokens string
	switch {
	case tokens >= 1_000_000:
		formattedTokens = fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		formattedTokens = fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		formattedTokens = fmt.Sprintf("%d", tokens)
	}

	// Remove .0 suffix if present
	if strings.HasSuffix(formattedTokens, ".0K") {
		formattedTokens = strings.Replace(formattedTokens, ".0K", "K", 1)
	}
	if strings.HasSuffix(formattedTokens, ".0M") {
		formattedTokens = strings.Replace(formattedTokens, ".0M", "M", 1)
	}

	// Session token totals are cumulative usage, not the model's live context
	// occupancy. Label them honestly and retain useful precision for inexpensive
	// runs that would otherwise appear to cost $0.00.
	formattedCost := "$0.00"
	switch {
	case cost == 0:
		// Keep the compact zero value.
	case cost > 0 && cost < 0.0001:
		formattedCost = "<$0.0001"
	case cost < 0.01:
		formattedCost = fmt.Sprintf("$%.4f", cost)
	default:
		formattedCost = fmt.Sprintf("$%.2f", cost)
	}

	return fmt.Sprintf("Usage: %s, Cost: %s", formattedTokens, formattedCost)
}

const (
	defaultStatusHint  = "enter send · alt+p actions · f1 help"
	minimumNoticeWidth = 12
)

// sanitizeStatusText turns arbitrary, potentially hostile text into a single
// terminal-safe line. Status messages share a one-row layout allocation with
// the active page, so preserving embedded newlines would corrupt the entire
// frame even after terminal escape sequences had been removed.
func sanitizeStatusText(value string) string {
	return strings.Join(strings.Fields(styles.SanitizeTerminalText(value)), " ")
}

// composeStatusLine fits the status segments in priority order. Notices and
// diagnostics are both protected on narrow terminals: diagnostics keep their
// compact counters while the (usually longer) notice yields width first.
// Optional segments consume only the remaining space, in caller-provided
// priority order. ansi.Truncate preserves styling reset sequences while using
// visible cell width rather than byte length.
func composeStatusLine(width int, notice, diagnostics string, optional []string, separator string) string {
	if width <= 0 {
		return ""
	}

	separatorWidth := lipgloss.Width(separator)
	segments := make([]string, 0, 2+len(optional))
	appendTruncated := func(segment string, budget int) {
		if segment == "" || budget <= 0 {
			return
		}
		segments = append(segments, ansi.Truncate(segment, budget, "…"))
	}

	switch {
	case notice != "" && diagnostics != "" && width > separatorWidth+1:
		available := width - separatorWidth
		minimumNotice := min(lipgloss.Width(notice), minimumNoticeWidth)
		if minimumNotice >= available {
			minimumNotice = max(1, available/2)
		}
		diagnosticsBudget := min(lipgloss.Width(diagnostics), max(1, available-minimumNotice))
		noticeBudget := max(1, available-diagnosticsBudget)
		appendTruncated(notice, noticeBudget)
		appendTruncated(diagnostics, diagnosticsBudget)
	case notice != "":
		appendTruncated(notice, width)
	case diagnostics != "":
		appendTruncated(diagnostics, width)
	}

	used := 0
	if len(segments) > 0 {
		used = lipgloss.Width(strings.Join(segments, separator))
	}
	for _, segment := range optional {
		if segment == "" {
			continue
		}
		budget := width - used
		if len(segments) > 0 {
			budget -= separatorWidth
		}
		if budget <= 0 {
			break
		}
		appendTruncated(segment, budget)
		used = lipgloss.Width(strings.Join(segments, separator))
		// A truncated segment consumes the remaining allocation. Lower-priority
		// segments must not displace it merely because they happen to be shorter.
		if lipgloss.Width(segment) > budget {
			break
		}
	}

	return ansi.Truncate(strings.Join(segments, separator), width, "…")
}

func (m statusCmp) View() string {
	t := theme.CurrentTheme()
	base := styles.BaseStyle()
	notice := ""
	if message := sanitizeStatusText(m.info.Msg); message != "" {
		color := t.Info()
		symbol := styles.Glyph("•", "i")
		switch m.info.Type {
		case util.InfoTypeWarn:
			color, symbol = t.Warning(), styles.Glyph("⚠", "!")
		case util.InfoTypeError:
			color, symbol = t.Error(), styles.Glyph("✖", "x")
		}
		notice = base.Foreground(color).Bold(true).Render(symbol) + " " + base.Foreground(t.Text()).Render(message)
	}

	diagnostics := m.projectDiagnostics()
	if diagnostics == "No diagnostics" {
		diagnostics = ""
	}

	optional := make([]string, 0, 3)
	cfg := config.Get()
	modelLabel := "model unavailable"
	if cfg != nil {
		if coder, ok := cfg.Agents[config.AgentCoder]; ok {
			if model, found := models.SupportedModels[coder.Model]; found {
				modelLabel = model.Name
				if m.session.ID != "" {
					totalTokens := m.session.PromptTokens + m.session.CompletionTokens
					optional = append(optional, base.Foreground(t.TextMuted()).Render(formatTokensAndCost(totalTokens, m.session.Cost)))
				}
			}
		}
	}
	optional = append(optional,
		base.Foreground(t.TextMuted()).Render(sanitizeStatusText(modelLabel)),
		base.Foreground(t.TextMuted()).Render(defaultStatusHint),
	)

	separator := base.Foreground(t.BorderDim()).Render("  ·  ")
	line := composeStatusLine(m.width, notice, diagnostics, optional, separator)
	if m.width <= 0 {
		return ""
	}
	return base.Width(m.width).MaxWidth(m.width).Render(line)
}

func (m *statusCmp) projectDiagnostics() string {
	t := theme.CurrentTheme()
	if m.lspClients == nil {
		return "No diagnostics"
	}
	clients := m.lspClients.Snapshot()

	// Check if any LSP server is still initializing
	initializing := false
	for _, client := range clients {
		if client.GetServerState() == lsp.StateStarting {
			initializing = true
			break
		}
	}

	// If any server is initializing, show that status
	if initializing {
		return lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Warning()).
			Render(fmt.Sprintf("%s Initializing LSP...", styles.SpinnerIcon))
	}

	errorDiagnostics := []protocol.Diagnostic{}
	warnDiagnostics := []protocol.Diagnostic{}
	hintDiagnostics := []protocol.Diagnostic{}
	infoDiagnostics := []protocol.Diagnostic{}
	for _, client := range clients {
		for _, d := range client.GetDiagnostics() {
			for _, diag := range d {
				switch diag.Severity {
				case protocol.SeverityError:
					errorDiagnostics = append(errorDiagnostics, diag)
				case protocol.SeverityWarning:
					warnDiagnostics = append(warnDiagnostics, diag)
				case protocol.SeverityHint:
					hintDiagnostics = append(hintDiagnostics, diag)
				case protocol.SeverityInformation:
					infoDiagnostics = append(infoDiagnostics, diag)
				}
			}
		}
	}

	if len(errorDiagnostics) == 0 && len(warnDiagnostics) == 0 && len(hintDiagnostics) == 0 && len(infoDiagnostics) == 0 {
		return "No diagnostics"
	}

	diagnostics := []string{}

	if len(errorDiagnostics) > 0 {
		errStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Error()).
			Render(fmt.Sprintf("%s %d", styles.ErrorIcon, len(errorDiagnostics)))
		diagnostics = append(diagnostics, errStr)
	}
	if len(warnDiagnostics) > 0 {
		warnStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Warning()).
			Render(fmt.Sprintf("%s %d", styles.WarningIcon, len(warnDiagnostics)))
		diagnostics = append(diagnostics, warnStr)
	}
	if len(hintDiagnostics) > 0 {
		hintStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Text()).
			Render(fmt.Sprintf("%s %d", styles.HintIcon, len(hintDiagnostics)))
		diagnostics = append(diagnostics, hintStr)
	}
	if len(infoDiagnostics) > 0 {
		infoStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Info()).
			Render(fmt.Sprintf("%s %d", styles.InfoIcon, len(infoDiagnostics)))
		diagnostics = append(diagnostics, infoStr)
	}

	return strings.Join(diagnostics, " ")
}

func (m statusCmp) availableFooterMsgWidth(diagnostics, tokenInfo string) int {
	tokensWidth := 0
	if m.session.ID != "" {
		tokensWidth = lipgloss.Width(tokenInfo) + 2
	}
	return max(0, m.width-lipgloss.Width(helpWidget)-lipgloss.Width(m.model())-lipgloss.Width(diagnostics)-tokensWidth)
}

func (m statusCmp) model() string {
	t := theme.CurrentTheme()

	cfg := config.Get()
	if cfg == nil {
		return "Unknown"
	}

	coder, ok := cfg.Agents[config.AgentCoder]
	if !ok {
		return "Unknown"
	}
	model := models.SupportedModels[coder.Model]

	return styles.BaseStyle().
		Foreground(t.TextMuted()).
		Render(model.Name)
}

func NewStatusCmp(lspClients map[string]*lsp.Client) StatusCmp {
	return NewStatusCmpWithClientProvider(lsp.NewStaticClientProvider(lspClients))
}

func NewStatusCmpWithClientProvider(lspClients lsp.ClientProvider) StatusCmp {
	helpWidget = getHelpWidget()

	return &statusCmp{
		messageTTL: 10 * time.Second,
		lspClients: lspClients,
	}
}
