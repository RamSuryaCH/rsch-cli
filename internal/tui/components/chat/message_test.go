package chat

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestRenderParamsIsUnicodeAndNarrowWidthSafe(t *testing.T) {
	for width := -5; width <= 14; width++ {
		got := renderParams(width, "报告👩🏽‍💻-café.go", "line", "42")
		if !utf8.ValidString(got) {
			t.Fatalf("renderParams(%d) returned invalid UTF-8: %q", width, got)
		}
		if lipgloss.Width(got) > max(0, width) {
			t.Fatalf("renderParams(%d) width = %d: %q", width, lipgloss.Width(got), got)
		}
	}
}

func TestTruncateHeightDisclosesOmittedLines(t *testing.T) {
	got := truncateHeight("one\ntwo\nthree\nfour", 3)
	if got != "one\n… 2 lines omitted\nfour" {
		t.Fatalf("truncateHeight() = %q", got)
	}
}

func TestTruncateHeightPreservesHeadTailAndSanitizes(t *testing.T) {
	content := "first\nsecond\nthird\nfourth\nfifth\n\x1b[31mlast\x1b[0m"
	got := truncateHeight(content, 4)
	if got != "first\nsecond\n… 3 lines omitted\nlast" {
		t.Fatalf("truncateHeight() = %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("truncateHeight() retained terminal control data: %q", got)
	}
}

func TestReasoningOnlyMessageShowsPrivateActivityState(t *testing.T) {
	const hiddenReasoning = "private chain of thought that must not be displayed"
	msg := message.Message{
		ID:    "assistant-reasoning",
		Role:  message.Assistant,
		State: message.StatePending,
		Parts: []message.ContentPart{message.ReasoningContent{Thinking: hiddenReasoning}},
	}

	rendered := renderAssistantMessage(msg, 0, nil, nil, msg.ID, false, 60, 0)
	if len(rendered) != 1 {
		t.Fatalf("renderAssistantMessage() returned %d blocks, want 1", len(rendered))
	}
	if !strings.Contains(rendered[0].content, "Thinking…") {
		t.Fatalf("reasoning activity state was not visible: %q", rendered[0].content)
	}
	if strings.Contains(rendered[0].content, hiddenReasoning) {
		t.Fatalf("reasoning activity exposed hidden model content: %q", rendered[0].content)
	}
}

func TestDisplayWorkspacePathRelativizesOnlyContainedPaths(t *testing.T) {
	workingDir := config.WorkingDirectory()
	inside := filepath.Join(workingDir, "internal", "tui", "chat.go")
	prefixCollision := filepath.Join(filepath.Dir(workingDir), filepath.Base(workingDir)+"-outside", "secret.go")
	relativeOutside := filepath.Join("..", "outside", "secret.go")

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "inside workspace", path: inside, want: filepath.Join("internal", "tui", "chat.go")},
		{name: "absolute outside workspace", path: prefixCollision, want: filepath.Clean(prefixCollision)},
		{name: "relative outside workspace", path: relativeOutside, want: relativeOutside},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayWorkspacePath(tc.path); got != tc.want {
				t.Fatalf("displayWorkspacePath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestMarkdownFenceCannotBeClosedByToolOutput(t *testing.T) {
	got := markdownFence("text", "before\n```\nafter")
	if !strings.HasPrefix(got, "````text\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("markdownFence did not choose a safe delimiter: %q", got)
	}
}

func TestFormatTimestampDiffUsesUnixSeconds(t *testing.T) {
	tests := map[string]struct {
		start, end int64
	}{
		"<1s":    {10, 10},
		"12s":    {10, 22},
		"2m 03s": {10, 133},
		"1h 01m": {0, 3661},
	}
	for want, tc := range tests {
		if got := formatTimestampDiff(tc.start, tc.end); got != want {
			t.Fatalf("formatTimestampDiff(%d, %d) = %q, want %q", tc.start, tc.end, got, want)
		}
	}
}
