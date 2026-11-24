package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/performance"
)

type budgetCaller struct{}

func (budgetCaller) Call(context.Context, Candidate, string) (string, error) { return "answer", nil }

func TestRouterTracksCostAndTransitions(t *testing.T) {
	tracker := performance.New(performance.Budget{MaxCost: 1})
	router := Router{Candidates: []Candidate{{Provider: "primary", Model: "a", CostPerMillion: 1000}}, Caller: budgetCaller{}, Budget: tracker}
	response, err := router.Call(context.Background(), "prompt")
	if err != nil || response.Cost <= 0 || len(response.AttemptsLog) != 1 {
		t.Fatalf("unexpected response: %+v %v", response, err)
	}
	limited := performance.New(performance.Budget{MaxCalls: 0, MaxCost: 0.000001})
	router.Budget = limited
	if _, err := router.Call(context.Background(), "long prompt"); !errors.Is(err, performance.ErrBudgetExceeded) {
		t.Fatalf("expected budget error, got %v", err)
	}
}

func TestRouterReportsCompletedAttemptWhenUsageExceedsBudget(t *testing.T) {
	tracker := performance.New(performance.Budget{MaxCost: 0.000001})
	router := Router{
		Candidates: []Candidate{{Provider: "primary", Model: "model", CostPerMillion: 1000}},
		Caller:     budgetCaller{},
		Budget:     tracker,
	}
	response, err := router.Call(context.Background(), "prompt")
	if !errors.Is(err, performance.ErrBudgetExceeded) {
		t.Fatalf("expected budget error, got %v", err)
	}
	if response.Attempts != 1 || len(response.AttemptsLog) != 1 {
		t.Fatalf("completed attempt missing from budget rejection: %+v", response)
	}
	if response.Cost != 0.012 {
		t.Fatalf("incurred cost = %v, want 0.012", response.Cost)
	}
	attempt := response.AttemptsLog[0]
	if attempt.Provider != "primary" || attempt.Model != "model" || attempt.Error != "" {
		t.Fatalf("completed provider attempt not preserved: %+v", attempt)
	}
}
