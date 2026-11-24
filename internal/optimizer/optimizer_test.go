package optimizer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/message"
)

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s line %04d\n", prefix, i)
	}
	return b.String()
}

func user(text string) message.Message {
	return message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

func call(id, name, input string) message.Message {
	return message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: id, Name: name, Input: input, Finished: true}}}
}

func result(id, name, content string) message.Message {
	return message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: id, Name: name, Content: content}}}
}

func content(t *testing.T, msg message.Message) string {
	t.Helper()
	for _, part := range msg.Parts {
		if r, ok := part.(message.ToolResult); ok {
			return r.Content
		}
	}
	t.Fatal("message has no tool result")
	return ""
}

func TestTrimMiddleKeepsHeadAndTailWithMarker(t *testing.T) {
	text := lines(1000, "out")
	trimmed := TrimMiddle(text, 2000)
	if len(trimmed) >= len(text) || len(trimmed) > 2300 {
		t.Fatalf("trimmed length = %d, want about 2000 (from %d)", len(trimmed), len(text))
	}
	if !strings.HasPrefix(trimmed, "out line 0000\n") || !strings.HasSuffix(trimmed, "out line 0999\n") {
		t.Fatalf("head or tail lost:\n%s", trimmed)
	}
	if !strings.Contains(trimmed, "[rsch optimizer: trimmed ") || !strings.Contains(trimmed, "lines (") {
		t.Fatalf("missing summary marker:\n%s", trimmed)
	}
	if TrimMiddle("short", 2000) != "short" {
		t.Fatal("short text must be unchanged")
	}
}

func TestTrimsStaleOutputsHarderThanRecentOnes(t *testing.T) {
	big := lines(800, "x") // about 11 KB
	history := []message.Message{
		user("go"),
		call("1", "bash", "{}"), result("1", "bash", big+"old"),
		call("2", "bash", "{}"), result("2", "bash", big+"mid"),
		call("3", "bash", "{}"), result("3", "bash", big+"new"),
	}
	o := New(Options{MaxToolOutputBytes: 16000, StaleToolOutputBytes: 2000, RecentToolMessages: 2})
	out, stats := o.Optimize("s", history, 0)
	if got := len(content(t, out[2])); got > 2300 {
		t.Fatalf("stale output kept %d bytes, want about 2000", got)
	}
	if content(t, out[4]) != big+"mid" || content(t, out[6]) != big+"new" {
		t.Fatal("recent outputs under the recent cap must be unchanged")
	}
	if stats.Trimmed != 1 || stats.TokensAfter >= stats.TokensBefore {
		t.Fatalf("stats = %+v", stats)
	}
	if content(t, history[2]) != big+"old" {
		t.Fatal("Optimize modified the caller's history")
	}
}

func TestDeduplicatesRepeatedBlocksKeepingTheLatest(t *testing.T) {
	file := lines(40, "package main // file")
	history := []message.Message{
		user("read it"),
		call("a", "view", `{"file_path":"main.go"}`), result("a", "view", file),
		call("b", "bash", `{}`), result("b", "bash", "ok"),
		call("c", "view", `{"file_path":"main.go"}`), result("c", "view", file),
	}
	out, stats := New(Options{}).Optimize("s", history, 0)
	if stats.Deduplicated != 1 {
		t.Fatalf("deduplicated = %d, want 1", stats.Deduplicated)
	}
	if got := content(t, out[2]); !strings.Contains(got, "identical to the later output of view (call c)") {
		t.Fatalf("older copy = %q", got)
	}
	if content(t, out[6]) != file {
		t.Fatal("latest copy must stay whole")
	}
	if content(t, out[4]) != "ok" {
		t.Fatal("short outputs must not be touched")
	}
}

func TestCacheGivesStableRewritesAcrossTurns(t *testing.T) {
	big := lines(900, "log")
	history := []message.Message{
		user("turn 1"), call("1", "bash", "{}"), result("1", "bash", big),
		call("2", "bash", "{}"), result("2", "bash", "a"),
		call("3", "bash", "{}"), result("3", "bash", "b"),
	}
	o := New(Options{StaleToolOutputBytes: 2000, RecentToolMessages: 2})
	first, firstStats := o.Optimize("s", history, 0)
	history = append(history, user("turn 2"), call("4", "bash", "{}"), result("4", "bash", "c"))
	second, secondStats := o.Optimize("s", history, 0)
	if firstStats.CacheHits != 0 || secondStats.CacheHits != 1 {
		t.Fatalf("cache hits = %d then %d, want 0 then 1", firstStats.CacheHits, secondStats.CacheHits)
	}
	if content(t, first[2]) != content(t, second[2]) {
		t.Fatal("the same block must be rewritten identically on later turns")
	}
	totals := o.Totals("s")
	if totals.Requests != 2 || totals.TokensBefore != firstStats.TokensBefore+secondStats.TokensBefore {
		t.Fatalf("totals = %+v", totals)
	}
	if other := o.Totals("other"); other.Requests != 0 {
		t.Fatalf("sessions must not share totals: %+v", other)
	}
}

func TestCompactsOldestOutputsToFitContextWindow(t *testing.T) {
	block := func(tag string) string { return lines(150, tag) } // about 2 KB each
	history := []message.Message{user("go")}
	for i := 0; i < 10; i++ {
		id := fmt.Sprint(i)
		history = append(history, call(id, "grep", "{}"), result(id, "grep", block(id)))
	}
	before := HistoryTokens(history)
	o := New(Options{StaleToolOutputBytes: 100000, MaxToolOutputBytes: 100000, CompactAtFraction: 0.5})
	window := int64(before) // budget is half of the current history
	out, stats := o.Optimize("s", history, window)
	if stats.Compacted == 0 || stats.TokensAfter > before/2 {
		t.Fatalf("stats = %+v, want history compacted under %d tokens", stats, before/2)
	}
	if !strings.Contains(content(t, out[2]), "omitted to fit the context window") {
		t.Fatal("oldest output should be compacted first")
	}
	if content(t, out[len(out)-1]) != block("9") {
		t.Fatal("recent outputs must not be compacted")
	}
	if len(out) != len(history) {
		t.Fatal("compaction must not drop messages")
	}
}

func TestOffSwitchSendsHistoryUnchanged(t *testing.T) {
	t.Setenv(DisableEnv, "off")
	history := []message.Message{user("go"), call("1", "bash", "{}"), result("1", "bash", lines(5000, "x"))}
	out, stats := New(Options{}).Optimize("s", history, 1000)
	if stats.Enabled || stats.TokensBefore != stats.TokensAfter || stats.Requests != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if content(t, out[2]) != content(t, history[2]) {
		t.Fatal("history changed while the optimizer was off")
	}
	t.Setenv(DisableEnv, "")
	if !Enabled() {
		t.Fatal("optimizer must be on by default")
	}
}

func TestToolResultIDsSurvive(t *testing.T) {
	history := []message.Message{user("go"), call("x1", "bash", "{}"), result("x1", "bash", lines(5000, "x"))}
	out, _ := New(Options{}).Optimize("s", history, 100)
	r := out[2].Parts[0].(message.ToolResult)
	if r.ToolCallID != "x1" || r.Name != "bash" {
		t.Fatalf("tool result identity changed: %+v", r)
	}
}
