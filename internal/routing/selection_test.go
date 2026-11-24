package routing

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/customagent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/project"
	"github.com/opencode-ai/opencode/internal/risk"
)

func TestClassifyHonorsSecurityAndVisionSignals(t *testing.T) {
	if got := Classify(Signals{Prompt: "review this screenshot"}).Class; got != Vision {
		t.Fatalf("expected vision, got %q", got)
	}
	if got := Classify(Signals{Prompt: "review auth token handling", Risk: risk.High}).Class; got != Security {
		t.Fatalf("expected security, got %q", got)
	}
	if got := Classify(Signals{Prompt: "rename this variable", Complexity: 1}).Class; got != Fast {
		t.Fatalf("expected fast, got %q", got)
	}
}

func TestOrderModelsPrefersRequiredCapability(t *testing.T) {
	plain := models.Model{ID: "plain", ContextWindow: 32_000, CostPer1MIn: 1, CostPer1MOut: 1}
	vision := models.Model{ID: "vision", ContextWindow: 16_000, SupportsAttachments: true, CostPer1MIn: 2, CostPer1MOut: 2}
	ordered := OrderModels(Vision, []models.Model{plain, vision}, Signals{HasAttachments: true})
	if len(ordered) != 1 || ordered[0].ID != vision.ID {
		t.Fatalf("expected only the attachment-capable model, got %+v", ordered)
	}
	if filtered := OrderModels(Coding, []models.Model{plain, vision}, Signals{CostBudget: 1.5}); len(filtered) != 0 {
		t.Fatalf("expected cost budget to reject all candidates, got %+v", filtered)
	}
}

func TestOrderModelsEnforcesReasoningCapability(t *testing.T) {
	plain := models.Model{ID: "plain", ContextWindow: 32_000, DefaultMaxTokens: 4_000}
	reasoning := models.Model{ID: "reasoning", ContextWindow: 32_000, DefaultMaxTokens: 4_000, CanReason: true}

	ordered := OrderModels(Reasoning, []models.Model{plain, reasoning}, Signals{Requirements: ModelRequirements{Reasoning: true}})
	if len(ordered) != 1 || ordered[0].ID != reasoning.ID {
		t.Fatalf("expected only reasoning-capable model, got %+v", ordered)
	}
}

func TestOrderModelsRejectsCandidatesThatCannotFitContext(t *testing.T) {
	small := models.Model{ID: "small", ContextWindow: 16_000, DefaultMaxTokens: 4_000}
	large := models.Model{ID: "large", ContextWindow: 64_000, DefaultMaxTokens: 8_000}
	unknown := models.Model{ID: "unknown", DefaultMaxTokens: 8_000}

	ordered := OrderModels(Coding, []models.Model{small, unknown, large}, Signals{ContextTokens: 32_000})
	if len(ordered) != 1 || ordered[0].ID != large.ID {
		t.Fatalf("expected only the context-compatible model, got %+v", ordered)
	}
}

func TestOrderModelsAccountsForRequestedOutputReserve(t *testing.T) {
	tight := models.Model{ID: "tight", ContextWindow: 32_000, DefaultMaxTokens: 8_000}
	roomy := models.Model{ID: "roomy", ContextWindow: 64_000, DefaultMaxTokens: 8_000}

	ordered := OrderModels(Coding, []models.Model{tight, roomy}, Signals{ContextTokens: 28_000, OutputTokens: 8_000})
	if len(ordered) != 1 || ordered[0].ID != roomy.ID {
		t.Fatalf("expected output reserve to reject tight model, got %+v", ordered)
	}
}

func TestOrderModelsRejectsOutputAboveModelLimit(t *testing.T) {
	smallOutput := models.Model{ID: "small-output", ContextWindow: 64_000, DefaultMaxTokens: 4_000}
	largeOutput := models.Model{ID: "large-output", ContextWindow: 64_000, DefaultMaxTokens: 16_000}

	ordered := OrderModels(Coding, []models.Model{smallOutput, largeOutput}, Signals{ContextTokens: 8_000, OutputTokens: 8_000})
	if len(ordered) != 1 || ordered[0].ID != largeOutput.ID {
		t.Fatalf("expected only model satisfying requested output reserve, got %+v", ordered)
	}
}

func TestOrderModelCandidatesEnforcesModalitiesAndCapabilities(t *testing.T) {
	base := models.Model{ID: "candidate", ContextWindow: 64_000, DefaultMaxTokens: 8_000}
	incomplete := ModelCandidate{
		Model: base,
		Capabilities: ModelCapabilities{
			InputModalities:  []Modality{ModalityText, ModalityAudio},
			OutputModalities: []Modality{ModalityText},
		},
	}
	complete := incomplete
	complete.Model.ID = "complete"
	complete.Capabilities.ToolCalls = true
	complete.Capabilities.StructuredOutput = true

	signals := Signals{Requirements: ModelRequirements{
		ToolCalls:        true,
		StructuredOutput: true,
		InputModalities:  []Modality{ModalityAudio},
		OutputModalities: []Modality{ModalityText},
	}}
	ordered := OrderModelCandidates(Coding, []ModelCandidate{incomplete, complete}, signals)
	if len(ordered) != 1 || ordered[0].Model.ID != complete.Model.ID {
		t.Fatalf("expected only fully capable model, got %+v", ordered)
	}

	eligibility := EvaluateModel(Coding, incomplete, signals)
	if eligibility.Eligible || len(eligibility.Reasons) != 2 {
		t.Fatalf("expected tool and structured-output rejection reasons, got %+v", eligibility)
	}
}

func TestExplicitAttachmentModalityDoesNotAssumeImage(t *testing.T) {
	candidate := ModelCandidate{
		Model: models.Model{ID: "pdf", ContextWindow: 64_000, DefaultMaxTokens: 8_000},
		Capabilities: ModelCapabilities{
			InputModalities:  []Modality{ModalityText, ModalityPDF},
			OutputModalities: []Modality{ModalityText},
		},
	}
	ordered := OrderModelCandidates(Vision, []ModelCandidate{candidate}, Signals{
		HasAttachments: true,
		Requirements:   ModelRequirements{InputModalities: []Modality{ModalityPDF}},
	})
	if len(ordered) != 1 {
		t.Fatalf("expected explicit PDF capability to satisfy attachment request, got %+v", ordered)
	}
}

func TestSelectRuleMakesRequestClassesReachable(t *testing.T) {
	rules := map[string]string{
		string(Coding):   "coding-models",
		string(Vision):   "vision-models",
		string(Review):   "review-models",
		string(Security): "security-models",
	}
	tests := []struct {
		name    string
		signals Signals
		class   TaskClass
		rule    string
	}{
		{name: "coding without complexity estimate", signals: Signals{Prompt: "implement pagination"}, class: Coding, rule: "coding-models"},
		{name: "vision", signals: Signals{HasAttachments: true}, class: Vision, rule: "vision-models"},
		{name: "review", signals: Signals{Prompt: "review this pull request"}, class: Review, rule: "review-models"},
		{name: "security", signals: Signals{Prompt: "audit credential handling"}, class: Security, rule: "security-models"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, rule, ok := SelectRule(rules, test.signals)
			if !ok || decision.Class != test.class || rule != test.rule {
				t.Fatalf("unexpected rule selection: decision=%+v rule=%q ok=%v", decision, rule, ok)
			}
		})
	}
}

func TestAgentRouterPreservesProviderConstraint(t *testing.T) {
	entry := customagent.Entry{Name: "coder", Definition: customagent.Definition{Manifest: project.Manifest{Role: "coder", Provider: "required"}}}
	available := []models.Model{
		{ID: "a-other", Provider: "other", ContextWindow: 32_000},
		{ID: "z-required", Provider: "required", ContextWindow: 32_000},
	}
	decision, err := NewAgentRouter([]customagent.Entry{entry}, available).Route(AgentRequest{Prompt: "implement pagination"})
	if err != nil || decision.Provider != "required" || decision.Model != "z-required" {
		t.Fatalf("provider constraint ignored: decision=%+v err=%v", decision, err)
	}
}

func TestAgentRouterRejectsIneligibleModelSelection(t *testing.T) {
	tests := []struct {
		name     string
		manifest project.Manifest
		request  AgentRequest
	}{
		{name: "no context fit", request: AgentRequest{ContextTokens: 64_000}},
		{name: "no attachment capability", request: AgentRequest{HasAttachments: true}},
		{name: "pinned model unavailable", manifest: project.Manifest{Model: "missing"}},
		{name: "pinned provider unavailable", manifest: project.Manifest{Provider: "missing"}},
		{name: "pinned model lacks capability", manifest: project.Manifest{Model: "plain"}, request: AgentRequest{HasAttachments: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := customagent.Entry{Name: "agent", Definition: customagent.Definition{Manifest: test.manifest}}
			available := []models.Model{{ID: "plain", Provider: "available", ContextWindow: 32_000}}
			decision, err := NewAgentRouter([]customagent.Entry{entry}, available).Route(test.request)
			if err == nil {
				t.Fatalf("ineligible model accepted: %+v", decision)
			}
		})
	}
}

func TestAgentRouterSkipsAgentWithUnusablePinnedModel(t *testing.T) {
	entries := []customagent.Entry{
		{Name: "a-coder", Definition: customagent.Definition{Manifest: project.Manifest{Role: "coder", Model: "unavailable"}}},
		{Name: "z-coder", Definition: customagent.Definition{Manifest: project.Manifest{Role: "coder", Model: "available"}}},
	}
	available := []models.Model{{ID: "available", Provider: "provider", ContextWindow: 32_000}}
	decision, err := NewAgentRouter(entries, available).Route(AgentRequest{Prompt: "implement pagination"})
	if err != nil || decision.Agent != "z-coder" || decision.Model != "available" || decision.Provider != "provider" {
		t.Fatalf("expected available agent and pinned model, decision=%+v err=%v", decision, err)
	}
}

func TestAgentRouterSelectsDeclarativeAgentAndExecutionPlan(t *testing.T) {
	entries := []customagent.Entry{
		{Name: "coder", Definition: customagent.Definition{Manifest: project.Manifest{Role: "coder", Skills: []string{"go"}, Tools: []string{"edit"}}}},
		{Name: "reviewer", Definition: customagent.Definition{Manifest: project.Manifest{Role: "security reviewer", Skills: []string{"security"}, Tools: []string{"view"}, Risk: "high"}, Subagents: []string{"scanner", "reporter"}, MaxConcurrency: 2}},
	}
	decision, err := NewAgentRouter(entries).Route(AgentRequest{Prompt: "review the authentication vulnerability", RequiredSkills: []string{"security"}, Risk: risk.High})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Agent != "reviewer" || decision.Class != Security || decision.Execution.Mode != "delegated-parallel" {
		t.Fatalf("unexpected routing decision: %+v", decision)
	}
}

func TestAgentRouterUsesAvailableModelWhenManifestDoesNotPinOne(t *testing.T) {
	entry := customagent.Entry{Name: "coder", Definition: customagent.Definition{Manifest: project.Manifest{Role: "coder"}}}
	model := models.Model{ID: "model", Provider: models.ProviderMock, ContextWindow: 32_000}
	decision, err := NewAgentRouter([]customagent.Entry{entry}, []models.Model{model}).Route(AgentRequest{Prompt: "implement this"})
	if err != nil || decision.Model != "model" || decision.Provider != string(models.ProviderMock) {
		t.Fatalf("unexpected model selection: %+v err=%v", decision, err)
	}
}
