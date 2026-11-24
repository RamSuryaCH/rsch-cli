package routing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/customagent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/risk"
)

// AgentRequest contains observable task requirements used to choose a
// declarative agent. It deliberately contains no private model reasoning.
type AgentRequest struct {
	Prompt               string
	RequiredCapabilities []string
	RequiredSkills       []string
	RequiredTools        []string
	Risk                 risk.Level
	Complexity           int
	ContextTokens        int64
	LatencyBudget        time.Duration
	CostBudget           float64
	HasAttachments       bool
	Override             TaskClass
}

// ExecutionPlan is the operational part of an agent routing decision. Zero
// limits mean the selected manifest did not declare that limit.
type ExecutionPlan struct {
	Mode           string   `json:"mode"`
	Subagents      []string `json:"subagents,omitempty"`
	MaxDepth       int      `json:"maxDepth,omitempty"`
	MaxAgents      int      `json:"maxAgents,omitempty"`
	MaxCost        float64  `json:"maxCost,omitempty"`
	MaxRuntimeNS   int64    `json:"maxRuntimeNs,omitempty"`
	MaxConcurrency int      `json:"maxConcurrency,omitempty"`
}

// AgentDecision is intentionally concise and safe to expose from a CLI or
// API. Reasons explain the operational selection criteria, not hidden model
// deliberation.
type AgentDecision struct {
	Class       TaskClass     `json:"class"`
	Agent       string        `json:"agent"`
	Role        string        `json:"role"`
	Skills      []string      `json:"skills,omitempty"`
	Tools       []string      `json:"tools,omitempty"`
	Permissions []string      `json:"permissions,omitempty"`
	Model       string        `json:"model,omitempty"`
	Provider    string        `json:"provider,omitempty"`
	Execution   ExecutionPlan `json:"execution"`
	Reasons     []string      `json:"reasons"`
}

// AgentRouter selects a filesystem-defined agent. When available models are
// supplied, both pinned and automatic choices must satisfy their capability
// constraints, and automatic choices preserve any manifest provider constraint.
type AgentRouter struct {
	Entries []customagent.Entry
	Models  []models.Model
}

func NewAgentRouter(entries []customagent.Entry, availableModels ...[]models.Model) AgentRouter {
	router := AgentRouter{Entries: append([]customagent.Entry(nil), entries...)}
	if len(availableModels) > 0 {
		router.Models = append([]models.Model(nil), availableModels[0]...)
	}
	return router
}

func (r AgentRouter) Route(request AgentRequest) (AgentDecision, error) {
	if len(r.Entries) == 0 {
		return AgentDecision{}, errors.New("no declarative agents configured")
	}
	signals := Signals{Prompt: request.Prompt, Complexity: request.Complexity, Risk: request.Risk, ContextTokens: request.ContextTokens, LatencyBudget: request.LatencyBudget, CostBudget: request.CostBudget, HasAttachments: request.HasAttachments, Override: request.Override}
	class := Classify(signals)
	type scored struct {
		entry  customagent.Entry
		model  *models.Model
		score  int
		reason []string
	}
	var candidates []scored
	for _, entry := range r.Entries {
		definition := entry.Definition
		if !matchesAll(request.RequiredCapabilities, append(append(append([]string{}, definition.Capabilities...), definition.Skills...), definition.Tools...)) {
			continue
		}
		if !matchesAll(request.RequiredSkills, definition.Skills) || !matchesAll(request.RequiredTools, definition.Tools) {
			continue
		}
		if request.Risk != "" && riskRank(definition.Risk) < riskRank(string(request.Risk)) {
			continue
		}
		var selectedModel *models.Model
		if len(r.Models) > 0 {
			available := make([]models.Model, 0, len(r.Models))
			for _, model := range r.Models {
				if definition.Model != "" && definition.Model != string(model.ID) {
					continue
				}
				if definition.Provider != "" && definition.Provider != string(model.Provider) {
					continue
				}
				available = append(available, model)
			}
			ordered := OrderModels(class.Class, available, signals)
			if len(ordered) == 0 {
				continue
			}
			selectedModel = &ordered[0]
		}
		score, reasons := scoreAgent(entry, class.Class, request.Prompt)
		candidates = append(candidates, scored{entry: entry, model: selectedModel, score: score, reason: reasons})
	}
	if len(candidates) == 0 {
		return AgentDecision{}, fmt.Errorf("no declarative agent satisfies the requested capabilities and available model constraints")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].entry.Name < candidates[j].entry.Name
	})
	selected := candidates[0].entry.Definition
	decision := AgentDecision{
		Class:       class.Class,
		Agent:       candidates[0].entry.Name,
		Role:        selected.Role,
		Skills:      append([]string(nil), selected.Skills...),
		Tools:       append([]string(nil), selected.Tools...),
		Permissions: append([]string(nil), selected.Permissions...),
		Model:       selected.Model,
		Provider:    selected.Provider,
		Execution: ExecutionPlan{
			Mode:           executionMode(selected.Subagents, selected.MaxConcurrency),
			Subagents:      append([]string(nil), selected.Subagents...),
			MaxDepth:       selected.MaxDepth,
			MaxAgents:      selected.MaxAgents,
			MaxCost:        selected.MaxCost,
			MaxRuntimeNS:   int64(selected.MaxRuntime),
			MaxConcurrency: selected.MaxConcurrency,
		},
		Reasons: append([]string(nil), candidates[0].reason...),
	}
	if model := candidates[0].model; model != nil {
		decision.Model = string(model.ID)
		decision.Provider = string(model.Provider)
		decision.Reasons = append(decision.Reasons, "selected an available model satisfying the task and manifest constraints")
	}
	return decision, nil
}

func scoreAgent(entry customagent.Entry, class TaskClass, prompt string) (int, []string) {
	definition := entry.Definition
	text := strings.ToLower(strings.Join([]string{entry.Name, definition.Role, definition.Description, definition.Instructions}, " "))
	score := 0
	reasons := []string{}
	for _, term := range classTerms(class) {
		if strings.Contains(text, term) {
			score += 3
			reasons = append(reasons, "agent profile matches task class")
			break
		}
	}
	for _, term := range strings.Fields(strings.ToLower(prompt)) {
		if len(term) >= 4 && strings.Contains(text, term) {
			score++
		}
	}
	if len(definition.Skills) > 0 {
		reasons = append(reasons, "agent declares reusable skills")
	}
	if len(definition.Tools) > 0 {
		reasons = append(reasons, "agent declares required tools")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "agent satisfies the declared requirements")
	}
	return score, reasons
}

func classTerms(class TaskClass) []string {
	switch class {
	case Fast:
		return []string{"fast", "quick", "title", "summar"}
	case Coding:
		return []string{"cod", "implement", "engineer"}
	case Reasoning:
		return []string{"architect", "plan", "reason", "debug"}
	case Vision:
		return []string{"vision", "browser", "visual"}
	case Review:
		return []string{"review", "audit", "test"}
	case Security:
		return []string{"security", "secure", "threat", "vulnerab"}
	default:
		return nil
	}
}

func executionMode(subagents []string, maxConcurrency int) string {
	switch {
	case len(subagents) == 0:
		return "single"
	case maxConcurrency == 1:
		return "delegated-sequential"
	default:
		return "delegated-parallel"
	}
}

func matchesAll(required, available []string) bool {
	set := make(map[string]struct{}, len(available))
	for _, value := range available {
		set[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range required {
		if _, ok := set[strings.ToLower(strings.TrimSpace(value))]; !ok {
			return false
		}
	}
	return true
}

func riskRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(risk.Critical):
		return 4
	case string(risk.High):
		return 3
	case string(risk.Medium):
		return 2
	case string(risk.Low):
		return 1
	default:
		return 0
	}
}
