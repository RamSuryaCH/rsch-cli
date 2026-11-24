package core

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

func TestStaleTimerDoesNotClearNewerStatus(t *testing.T) {
	model := statusCmp{messageTTL: time.Second}
	updated, _ := model.Update(util.InfoMsg{Msg: "first"})
	model = updated.(statusCmp)
	firstGeneration := model.generation

	updated, _ = model.Update(util.InfoMsg{Msg: "second"})
	model = updated.(statusCmp)
	secondGeneration := model.generation

	updated, _ = model.Update(util.ClearStatusMsg{Generation: firstGeneration})
	model = updated.(statusCmp)
	if model.info.Msg != "second" {
		t.Fatalf("stale timer cleared newer status: %#v", model.info)
	}

	updated, _ = model.Update(util.ClearStatusMsg{Generation: secondGeneration})
	model = updated.(statusCmp)
	if model.info.Msg != "" {
		t.Fatalf("current timer did not clear status: %#v", model.info)
	}
}

func TestStatusViewIsOneSanitizedBoundedRow(t *testing.T) {
	hostile := "provider failed\nsecond line\twith tab\x1b]52;c;c2VjcmV0\a\x1b[2J\r\b\u202espoofed"

	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			model := NewStatusCmp(nil).(*statusCmp)
			model.width = width
			model.info = util.InfoMsg{Type: util.InfoTypeError, Msg: hostile}
			model.session = session.Session{ID: "session", PromptTokens: 12_000, CompletionTokens: 2_000, Cost: 0.25}

			view := model.View()
			plain := ansi.Strip(view)
			if got := lipgloss.Height(view); got != 1 {
				t.Fatalf("height = %d, want 1; view=%q", got, view)
			}
			if got := lipgloss.Width(view); got > width {
				t.Fatalf("width = %d, want <= %d; view=%q", got, width, view)
			}
			if strings.ContainsAny(plain, "\n\r\t\x1b\a\b") {
				t.Fatalf("view retains row-breaking or terminal controls: %q", plain)
			}
			if strings.Contains(plain, "\u202e") {
				t.Fatalf("view retains bidi control: %q", plain)
			}
			if !strings.Contains(plain, "provider failed") {
				t.Fatalf("critical notice was lost at width %d: %q", width, plain)
			}
		})
	}
}

func TestComposeStatusLinePreservesCriticalSegmentsByWidth(t *testing.T) {
	separator := "  ·  "
	notice := "\x1b[31m✖ provider stream disconnected after a very long response\x1b[0m"
	diagnostics := "\x1b[33mE:2 W:3\x1b[0m"
	optional := []string{"Context: 72K, Cost: $0.42", "large-model", defaultStatusHint}

	for _, width := range []int{40, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			line := composeStatusLine(width, notice, diagnostics, optional, separator)
			plain := ansi.Strip(line)
			if got := lipgloss.Height(line); got != 1 {
				t.Fatalf("height = %d, want 1; line=%q", got, line)
			}
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width = %d, want <= %d; line=%q", got, width, line)
			}
			if !strings.Contains(plain, "provider") {
				t.Fatalf("notice missing at width %d: %q", width, plain)
			}
			if !strings.Contains(plain, "E:2 W:3") {
				t.Fatalf("diagnostics missing at width %d: %q", width, plain)
			}
		})
	}
}

func TestSanitizeStatusTextCollapsesUntrustedInput(t *testing.T) {
	input := "first\nsecond\tthird\x1b[2J\x1b]0;title\a\u202efourth"
	got := sanitizeStatusText(input)
	if got != "first second thirdfourth" {
		t.Fatalf("sanitizeStatusText() = %q", got)
	}
	if got != styles.SanitizeTerminalText(got) {
		t.Fatalf("sanitizeStatusText() returned unsafe text: %q", got)
	}
}

func TestFormatTokensAndCostLabelsCumulativeUsageHonestly(t *testing.T) {
	tests := []struct {
		name   string
		tokens int64
		cost   float64
		want   string
	}{
		{name: "whole tokens", tokens: 950, cost: 0, want: "Usage: 950, Cost: $0.00"},
		{name: "thousands", tokens: 12_000, cost: 0.0042, want: "Usage: 12K, Cost: $0.0042"},
		{name: "tiny nonzero cost", tokens: 2_000_000, cost: 0.00001, want: "Usage: 2M, Cost: <$0.0001"},
		{name: "normal cost", tokens: 1_200, cost: 1.25, want: "Usage: 1.2K, Cost: $1.25"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTokensAndCost(tt.tokens, tt.cost); got != tt.want {
				t.Fatalf("formatTokensAndCost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusViewWithStartingLSPRemainsBounded(t *testing.T) {
	client := &lsp.Client{}
	client.SetServerState(lsp.StateStarting)
	model := NewStatusCmp(map[string]*lsp.Client{"test": client}).(*statusCmp)
	model.width = 40
	model.info = util.InfoMsg{Type: util.InfoTypeWarn, Msg: "waiting\nfor permission"}

	view := model.View()
	plain := ansi.Strip(view)
	if lipgloss.Height(view) != 1 || lipgloss.Width(view) > model.width {
		t.Fatalf("status is not one bounded row: width=%d height=%d view=%q", lipgloss.Width(view), lipgloss.Height(view), view)
	}
	if !strings.Contains(plain, "waiting") || !strings.Contains(plain, "Initializing LSP") {
		t.Fatalf("notice or diagnostics were lost: %q", plain)
	}
}
