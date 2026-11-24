package routing

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/risk"
)

// TaskClass is the capability profile required by a model request.
type TaskClass string

const (
	Fast      TaskClass = "fast"
	Coding    TaskClass = "coding"
	Reasoning TaskClass = "reasoning"
	Vision    TaskClass = "vision"
	Review    TaskClass = "review"
	Security  TaskClass = "security"
)

// Signals are deliberately coarse and inspectable. They guide model choice
// without exposing private model reasoning or silently overriding policy.
type Signals struct {
	Prompt         string
	Complexity     int // zero means no complexity estimate was supplied
	Risk           risk.Level
	ContextTokens  int64 // rendered input tokens required by the request
	OutputTokens   int64 // output-token reserve required by the request
	LatencyBudget  time.Duration
	CostBudget     float64
	HasAttachments bool
	Override       TaskClass
	Requirements   ModelRequirements
}

// Modality is a provider-neutral model input or output media type.
type Modality string

const (
	ModalityText  Modality = "text"
	ModalityImage Modality = "image"
	ModalityAudio Modality = "audio"
	ModalityVideo Modality = "video"
	ModalityPDF   Modality = "pdf"
)

// ModelRequirements are hard constraints. Unknown capabilities fail closed.
type ModelRequirements struct {
	Reasoning        bool
	ToolCalls        bool
	StructuredOutput bool
	InputModalities  []Modality
	OutputModalities []Modality
}

// ModelCapabilities normalizes provider-specific capabilities for routing.
type ModelCapabilities struct {
	Reasoning        bool
	ToolCalls        bool
	StructuredOutput bool
	InputModalities  []Modality
	OutputModalities []Modality
}

type ModelCandidate struct {
	Model        models.Model
	Capabilities ModelCapabilities
}

// Eligibility explains a fail-closed candidate decision without exposing
// prompt text or credentials.
type Eligibility struct {
	Eligible bool
	Reasons  []string
}

type Decision struct {
	Class   TaskClass `json:"class"`
	Reasons []string  `json:"reasons"`
}

// Classify maps explicit overrides and observable request signals to a stable
// task class. The ordering is intentional: security and vision constraints
// take precedence over generic coding/reasoning language.
func Classify(signals Signals) Decision {
	if validClass(signals.Override) {
		return Decision{Class: signals.Override, Reasons: []string{"explicit routing override"}}
	}
	lower := strings.ToLower(signals.Prompt)
	if signals.HasAttachments || containsAny(lower, "screenshot", "image", "visual", "pixel", "layout") {
		return Decision{Class: Vision, Reasons: []string{"request contains visual evidence or attachments"}}
	}
	if signals.Risk == risk.High || signals.Risk == risk.Critical || containsAny(lower, "security", "vulnerability", "secret", "credential", "auth", "cve", "threat") {
		return Decision{Class: Security, Reasons: []string{"request is security-sensitive"}}
	}
	if containsAny(lower, "review", "inspect diff", "pull request", "code quality", "regression") {
		return Decision{Class: Review, Reasons: []string{"request asks for review or regression analysis"}}
	}
	if containsAny(lower, "architecture", "design", "root cause", "debug", "reason", "tradeoff", "plan") || signals.Complexity >= 7 {
		return Decision{Class: Reasoning, Reasons: []string{"request requires analysis or elevated complexity"}}
	}
	if containsAny(lower, "summarize", "title", "rename", "format", "list", "quick", "autocomplete") || signals.Complexity == 1 {
		return Decision{Class: Fast, Reasons: []string{"request is short or latency-sensitive"}}
	}
	return Decision{Class: Coding, Reasons: []string{"request is a general implementation task"}}
}

// SelectRule classifies a request and retrieves that class's configured rule.
// Generic string keys permit direct use with configuration maps while keeping
// this package independent from the config package.
func SelectRule[K ~string, T any](rules map[K]T, signals Signals) (Decision, T, bool) {
	decision := Classify(signals)
	rule, ok := rules[K(decision.Class)]
	return decision, rule, ok
}

// DefaultModelCapabilities conservatively adapts the legacy model catalog.
// Capabilities absent from models.Model remain false so explicit requirements
// cannot be satisfied accidentally.
func DefaultModelCapabilities(model models.Model) ModelCapabilities {
	input := []Modality{ModalityText}
	if model.SupportsAttachments {
		input = append(input, ModalityImage)
	}
	return ModelCapabilities{
		Reasoning:        model.CanReason,
		InputModalities:  input,
		OutputModalities: []Modality{ModalityText},
	}
}

// EvaluateModel applies hard capability, context, output-limit, modality, and
// cost constraints. Ranking happens only after this gate succeeds.
func EvaluateModel(class TaskClass, candidate ModelCandidate, signals Signals) Eligibility {
	requirements := effectiveRequirements(class, signals)
	reasons := make([]string, 0, 6)

	if requirements.Reasoning && !candidate.Capabilities.Reasoning {
		reasons = append(reasons, "reasoning capability is required")
	}
	if requirements.ToolCalls && !candidate.Capabilities.ToolCalls {
		reasons = append(reasons, "tool-call capability is required")
	}
	if requirements.StructuredOutput && !candidate.Capabilities.StructuredOutput {
		reasons = append(reasons, "structured-output capability is required")
	}
	for _, modality := range requirements.InputModalities {
		if !supportsModality(candidate.Capabilities.InputModalities, modality) {
			reasons = append(reasons, fmt.Sprintf("input modality %q is required", normalizeModality(modality)))
		}
	}
	for _, modality := range requirements.OutputModalities {
		if !supportsModality(candidate.Capabilities.OutputModalities, modality) {
			reasons = append(reasons, fmt.Sprintf("output modality %q is required", normalizeModality(modality)))
		}
	}
	if signals.ContextTokens > 0 || signals.OutputTokens > 0 {
		if !candidate.Model.FitsContext(signals.ContextTokens, signals.OutputTokens) {
			reasons = append(reasons, "request exceeds the model context window")
		}
	}
	if signals.OutputTokens > 0 && (candidate.Model.DefaultMaxTokens <= 0 || signals.OutputTokens > candidate.Model.DefaultMaxTokens) {
		reasons = append(reasons, "requested output exceeds the model output limit")
	}
	if signals.CostBudget > 0 && candidate.Model.CostPer1MIn+candidate.Model.CostPer1MOut > signals.CostBudget {
		reasons = append(reasons, "model exceeds the request cost ceiling")
	}

	return Eligibility{Eligible: len(reasons) == 0, Reasons: reasons}
}

// OrderModelCandidates filters ineligible candidates before deterministic
// scoring. Capability scores can never override hard requirements.
func OrderModelCandidates(class TaskClass, candidates []ModelCandidate, signals Signals) []ModelCandidate {
	ordered := make([]ModelCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if EvaluateModel(class, candidate, signals).Eligible {
			ordered = append(ordered, candidate)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		iScore := modelScore(class, ordered[i].Model, signals)
		jScore := modelScore(class, ordered[j].Model, signals)
		if iScore != jScore {
			return iScore > jScore
		}
		iCost := ordered[i].Model.CostPer1MIn + ordered[i].Model.CostPer1MOut
		jCost := ordered[j].Model.CostPer1MIn + ordered[j].Model.CostPer1MOut
		if iCost != jCost {
			return iCost < jCost
		}
		return string(ordered[i].Model.ID) < string(ordered[j].Model.ID)
	})
	return ordered
}

// OrderModels preserves the model-only API while applying the same hard gate
// with conservatively derived capabilities.
func OrderModels(class TaskClass, candidates []models.Model, signals Signals) []models.Model {
	profiled := make([]ModelCandidate, 0, len(candidates))
	for _, model := range candidates {
		profiled = append(profiled, ModelCandidate{Model: model, Capabilities: DefaultModelCapabilities(model)})
	}
	selected := OrderModelCandidates(class, profiled, signals)
	ordered := make([]models.Model, 0, len(selected))
	for _, candidate := range selected {
		ordered = append(ordered, candidate.Model)
	}
	return ordered
}

func effectiveRequirements(class TaskClass, signals Signals) ModelRequirements {
	requirements := signals.Requirements
	requirements.InputModalities = append([]Modality(nil), requirements.InputModalities...)
	requirements.OutputModalities = append([]Modality(nil), requirements.OutputModalities...)
	hasExplicitInputModality := len(requirements.InputModalities) > 0
	requirements.InputModalities = appendModality(requirements.InputModalities, ModalityText)
	requirements.OutputModalities = appendModality(requirements.OutputModalities, ModalityText)
	if (class == Vision || signals.HasAttachments) && !hasExplicitInputModality {
		requirements.InputModalities = appendModality(requirements.InputModalities, ModalityImage)
	}
	return requirements
}

func appendModality(modalities []Modality, modality Modality) []Modality {
	if supportsModality(modalities, modality) {
		return modalities
	}
	return append(modalities, modality)
}

func supportsModality(supported []Modality, required Modality) bool {
	required = normalizeModality(required)
	if required == "" {
		return false
	}
	for _, modality := range supported {
		if normalizeModality(modality) == required {
			return true
		}
	}
	return false
}

func normalizeModality(modality Modality) Modality {
	return Modality(strings.ToLower(strings.TrimSpace(string(modality))))
}

func modelScore(class TaskClass, model models.Model, signals Signals) int {
	score := 0
	switch class {
	case Fast:
		if model.DefaultMaxTokens > 0 {
			score += 2
		}
	case Coding:
		if model.ContextWindow >= 16_000 {
			score += 2
		}
		if model.DefaultMaxTokens >= 8_000 {
			score++
		}
	case Reasoning, Security, Review:
		if model.CanReason {
			score += 4
		}
		if model.ContextWindow >= 32_000 {
			score += 2
		}
	case Vision:
		if model.SupportsAttachments {
			score += 5
		}
	}
	if signals.ContextTokens > 0 && model.FitsContext(signals.ContextTokens, signals.OutputTokens) {
		score += 2
	}
	if signals.HasAttachments && model.SupportsAttachments {
		score += 3
	}
	if signals.LatencyBudget > 0 && model.DefaultMaxTokens > 0 && signals.LatencyBudget <= 10*time.Second {
		if class == Fast {
			score++
		}
	}
	if signals.CostBudget > 0 && model.CostPer1MIn+model.CostPer1MOut <= signals.CostBudget {
		score++
	}
	return score
}

func validClass(class TaskClass) bool {
	switch class {
	case Fast, Coding, Reasoning, Vision, Review, Security:
		return true
	default:
		return false
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
