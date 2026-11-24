package models

import "testing"

func TestSupportedModelsDeclareUsableTokenLimits(t *testing.T) {
	for id, model := range SupportedModels {
		if model.ContextWindow <= 0 {
			t.Errorf("model %q has no context window", id)
		}
		if model.DefaultMaxTokens <= 0 {
			t.Errorf("model %q has no default output limit", id)
		}
		if model.ContextWindow > 0 && model.DefaultMaxTokens > model.ContextWindow {
			t.Errorf("model %q default output limit %d exceeds context window %d", id, model.DefaultMaxTokens, model.ContextWindow)
		}
	}
}

func TestEffectiveMaxTokensUsesCandidateLimits(t *testing.T) {
	model := Model{ContextWindow: 32_000, DefaultMaxTokens: 8_000}
	tests := []struct {
		name          string
		requested     int64
		contextTokens int64
		want          int64
	}{
		{name: "candidate default", want: 8_000},
		{name: "lower explicit limit", requested: 4_000, want: 4_000},
		{name: "candidate output cap", requested: 16_000, want: 8_000},
		{name: "remaining context", requested: 8_000, contextTokens: 30_000, want: 2_000},
		{name: "full context", requested: 8_000, contextTokens: 32_000, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := model.EffectiveMaxTokens(test.requested, test.contextTokens); got != test.want {
				t.Fatalf("EffectiveMaxTokens(%d, %d) = %d, want %d", test.requested, test.contextTokens, got, test.want)
			}
		})
	}
	if got := (Model{DefaultMaxTokens: 8_000}).EffectiveMaxTokens(4_000, 0); got != 0 {
		t.Fatalf("unknown context limit must fail closed, got %d", got)
	}
	if got := (Model{ContextWindow: 8_000}).EffectiveMaxTokens(4_000, 0); got != 0 {
		t.Fatalf("unknown output limit must fail closed, got %d", got)
	}
}

func TestCachePricesUseWriteAndReadCategories(t *testing.T) {
	openAI := OpenAIModels[GPT41]
	if openAI.CostPer1MInCached != 0 || openAI.CostPer1MOutCached != 0.50 {
		t.Fatalf("OpenAI cache prices = write %.2f/read %.2f, want 0/0.50", openAI.CostPer1MInCached, openAI.CostPer1MOutCached)
	}
	mini := OpenAIModels[GPT41Mini]
	if mini.APIModel != "gpt-4.1-mini" {
		t.Fatalf("GPT-4.1 mini API model = %q", mini.APIModel)
	}
	if got := AzureModels[AzureGPT41].CostPer1MOutCached; got != openAI.CostPer1MOutCached {
		t.Fatalf("derived Azure cache-read price = %.2f, want %.2f", got, openAI.CostPer1MOutCached)
	}
	claude := AnthropicModels[Claude37Sonnet]
	if claude.CostPer1MInCached != 3.75 || claude.CostPer1MOutCached != 0.30 {
		t.Fatalf("Anthropic cache prices = write %.2f/read %.2f, want 3.75/0.30", claude.CostPer1MInCached, claude.CostPer1MOutCached)
	}

	priced := Model{
		CostPer1MIn:        1,
		CostPer1MOut:       2,
		CostPer1MInCached:  3,
		CostPer1MOutCached: 4,
	}
	if got, want := priced.UsageCost(1_000_000, 1_000_000, 1_000_000, 1_000_000), 10.0; got != want {
		t.Fatalf("UsageCost() = %.2f, want %.2f", got, want)
	}
}
