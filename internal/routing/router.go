// Package routing adds bounded model selection and fallback above the existing
// provider implementations.
package routing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/opencode-ai/opencode/internal/performance"
)

type Candidate struct {
	Provider       string        `json:"provider"`
	Model          string        `json:"model"`
	CostPerMillion float64       `json:"costPerMillion,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
}
type Response struct {
	Provider    string        `json:"provider"`
	Model       string        `json:"model"`
	Text        string        `json:"text"`
	Attempts    int           `json:"attempts"`
	Duration    time.Duration `json:"duration"`
	Cost        float64       `json:"cost,omitempty"`
	AttemptsLog []Attempt     `json:"attemptLog,omitempty"`
}
type Attempt struct {
	Provider string        `json:"provider"`
	Model    string        `json:"model"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}
type Caller interface {
	Call(context.Context, Candidate, string) (string, error)
}
type Router struct {
	Candidates  []Candidate
	MaxAttempts int
	Caller      Caller
	Budget      *performance.Tracker
}

func (r Router) Call(ctx context.Context, prompt string) (Response, error) {
	if r.Caller == nil {
		return Response{}, errors.New("model caller is required")
	}
	if len(r.Candidates) == 0 {
		return Response{}, errors.New("no model candidates configured")
	}
	limit := r.MaxAttempts
	if limit <= 0 || limit > len(r.Candidates) {
		limit = len(r.Candidates)
	}
	started := time.Now()
	var last error
	attemptLog := make([]Attempt, 0, limit)
	var totalCost float64
	for i, candidate := range r.Candidates[:limit] {
		if err := ctx.Err(); err != nil {
			return Response{Attempts: i, Duration: time.Since(started), AttemptsLog: attemptLog, Cost: totalCost}, err
		}
		if r.Budget != nil {
			if err := r.Budget.Consume(1, 0, 0); err != nil {
				return Response{Attempts: i, Duration: time.Since(started), AttemptsLog: attemptLog, Cost: totalCost}, err
			}
		}
		attemptStarted := time.Now()
		callCtx := ctx
		cancel := func() {}
		if candidate.Timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, candidate.Timeout)
		}
		text, err := r.Caller.Call(callCtx, candidate, prompt)
		cancel()
		if err == nil {
			cost := candidate.CostPerMillion * float64(len(prompt)+len(text)) / 1_000_000
			// The call has already completed and incurred usage even if the
			// resulting charge is rejected by the budget tracker.
			totalCost += cost
			attemptLog = append(attemptLog, Attempt{Provider: candidate.Provider, Model: candidate.Model, Duration: time.Since(attemptStarted)})
			if r.Budget != nil {
				if budgetErr := r.Budget.Consume(0, int64(len(prompt)+len(text)), cost); budgetErr != nil {
					return Response{Attempts: i + 1, Duration: time.Since(started), AttemptsLog: attemptLog, Cost: totalCost}, budgetErr
				}
			}
			return Response{Provider: candidate.Provider, Model: candidate.Model, Text: text, Attempts: i + 1, Duration: time.Since(started), Cost: totalCost, AttemptsLog: attemptLog}, nil
		}
		last = err
		attemptLog = append(attemptLog, Attempt{Provider: candidate.Provider, Model: candidate.Model, Error: err.Error(), Duration: time.Since(attemptStarted)})
		if ctx.Err() != nil {
			return Response{Attempts: i + 1, Duration: time.Since(started), AttemptsLog: attemptLog, Cost: totalCost}, ctx.Err()
		}
	}
	return Response{Attempts: limit, Duration: time.Since(started), AttemptsLog: attemptLog, Cost: totalCost}, fmt.Errorf("all model candidates failed: %w", last)
}
