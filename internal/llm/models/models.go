package models

import "maps"

type (
	ModelID       string
	ModelProvider string
)

type Model struct {
	ID           ModelID       `json:"id"`
	Name         string        `json:"name"`
	Provider     ModelProvider `json:"provider"`
	APIModel     string        `json:"api_model"`
	CostPer1MIn  float64       `json:"cost_per_1m_in"`
	CostPer1MOut float64       `json:"cost_per_1m_out"`
	// CostPer1MInCached is the price for writing one million input tokens to
	// the provider cache. CostPer1MOutCached is the price for reading one
	// million cached input tokens. The legacy field names are retained for
	// configuration compatibility; neither value describes output tokens.
	CostPer1MInCached   float64 `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached  float64 `json:"cost_per_1m_out_cached"`
	ContextWindow       int64   `json:"context_window"`
	DefaultMaxTokens    int64   `json:"default_max_tokens"`
	CanReason           bool    `json:"can_reason"`
	SupportsAttachments bool    `json:"supports_attachments"`
}

// FitsContext reports whether a request and its output reserve fit within the
// model's declared context window. Unknown limits fail closed so a model is
// never selected merely because its metadata is incomplete.
func (m Model) FitsContext(inputTokens, outputTokens int64) bool {
	if m.ContextWindow <= 0 || inputTokens < 0 || outputTokens < 0 {
		return false
	}
	return inputTokens <= m.ContextWindow-outputTokens
}

// EffectiveMaxTokens returns a candidate-specific output limit. A configured
// request may lower a model's default, but cannot raise it or consume tokens
// already occupied by the request context. This keeps fallback models from
// inheriting an unsafe output limit from the primary model.
func (m Model) EffectiveMaxTokens(requested, contextTokens int64) int64 {
	if m.ContextWindow <= 0 || m.DefaultMaxTokens <= 0 {
		return 0
	}
	limit := requested
	if limit <= 0 || limit > m.DefaultMaxTokens {
		limit = m.DefaultMaxTokens
	}
	if limit <= 0 || contextTokens < 0 {
		return 0
	}
	remaining := m.ContextWindow - contextTokens
	if remaining <= 0 {
		return 0
	}
	if limit > remaining {
		limit = remaining
	}
	return limit
}

// UsageCost applies the four token categories to their corresponding prices.
// Cache reads and writes are input-token categories, despite the legacy field
// names retained by Model.
func (m Model) UsageCost(inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens int64) float64 {
	return m.CostPer1MIn/1e6*float64(inputTokens) +
		m.CostPer1MOut/1e6*float64(outputTokens) +
		m.CostPer1MInCached/1e6*float64(cacheWriteTokens) +
		m.CostPer1MOutCached/1e6*float64(cacheReadTokens)
}

// Model IDs
const ( // GEMINI
	// Bedrock
	BedrockClaude37Sonnet ModelID = "bedrock.claude-3.7-sonnet"
)

const (
	ProviderBedrock ModelProvider = "bedrock"
	// ForTests
	ProviderMock ModelProvider = "__mock"
)

// Providers in order of popularity
var ProviderPopularity = map[ModelProvider]int{
	ProviderCopilot:    1,
	ProviderAnthropic:  2,
	ProviderOpenAI:     3,
	ProviderGemini:     4,
	ProviderGROQ:       5,
	ProviderOpenRouter: 6,
	ProviderBedrock:    7,
	ProviderAzure:      8,
	ProviderVertexAI:   9,
}

var SupportedModels = map[ModelID]Model{
	//
	// // GEMINI
	// GEMINI25: {
	// 	ID:                 GEMINI25,
	// 	Name:               "Gemini 2.5 Pro",
	// 	Provider:           ProviderGemini,
	// 	APIModel:           "gemini-2.5-pro-exp-03-25",
	// 	CostPer1MIn:        0,
	// 	CostPer1MInCached:  0,
	// 	CostPer1MOutCached: 0,
	// 	CostPer1MOut:       0,
	// },
	//
	// GRMINI20Flash: {
	// 	ID:                 GRMINI20Flash,
	// 	Name:               "Gemini 2.0 Flash",
	// 	Provider:           ProviderGemini,
	// 	APIModel:           "gemini-2.0-flash",
	// 	CostPer1MIn:        0.1,
	// 	CostPer1MInCached:  0,
	// 	CostPer1MOutCached: 0.025,
	// 	CostPer1MOut:       0.4,
	// },
	//
	// // Bedrock
	BedrockClaude37Sonnet: {
		ID:                  BedrockClaude37Sonnet,
		Name:                "Bedrock: Claude 3.7 Sonnet",
		Provider:            ProviderBedrock,
		APIModel:            "anthropic.claude-3-7-sonnet-20250219-v1:0",
		CostPer1MIn:         3.0,
		CostPer1MInCached:   3.75,
		CostPer1MOutCached:  0.30,
		CostPer1MOut:        15.0,
		ContextWindow:       200_000,
		DefaultMaxTokens:    50_000,
		CanReason:           true,
		SupportsAttachments: true,
	},
}

func init() {
	maps.Copy(SupportedModels, AnthropicModels)
	maps.Copy(SupportedModels, OpenAIModels)
	maps.Copy(SupportedModels, GeminiModels)
	maps.Copy(SupportedModels, GroqModels)
	maps.Copy(SupportedModels, AzureModels)
	maps.Copy(SupportedModels, OpenRouterModels)
	maps.Copy(SupportedModels, XAIModels)
	maps.Copy(SupportedModels, VertexAIGeminiModels)
	maps.Copy(SupportedModels, CopilotModels)
}
