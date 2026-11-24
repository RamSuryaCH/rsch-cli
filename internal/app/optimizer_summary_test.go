package app

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/optimizer"
)

func TestOptimizerSummaryReportsBeforeAndAfter(t *testing.T) {
	got := optimizerSummary(optimizer.Stats{Enabled: true, Requests: 3, TokensBefore: 1000, TokensAfter: 750, Trimmed: 2})
	for _, want := range []string{"saved ~250 of ~1000", "(25.0%)", "3 requests", "estimate", "2 trimmed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
	if off := optimizerSummary(optimizer.Stats{Requests: 1, TokensBefore: 10, TokensAfter: 10}); !strings.Contains(off, "optimizer off") {
		t.Fatalf("off summary = %q", off)
	}
}
