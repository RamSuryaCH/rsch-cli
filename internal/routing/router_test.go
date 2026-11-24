package routing

import (
	"context"
	"errors"
	"testing"
)

type fakeCaller struct{ calls int }

type callerFunc func(context.Context, Candidate, string) (string, error)

func (f callerFunc) Call(ctx context.Context, candidate Candidate, prompt string) (string, error) {
	return f(ctx, candidate, prompt)
}

func TestRouterDoesNotCallProviderAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	router := Router{
		Candidates: []Candidate{{Provider: "provider", Model: "model"}},
		Caller: callerFunc(func(context.Context, Candidate, string) (string, error) {
			calls++
			return "answer", nil
		}),
	}
	response, err := router.Call(ctx, "prompt")
	if !errors.Is(err, context.Canceled) || calls != 0 || response.Attempts != 0 || len(response.AttemptsLog) != 0 {
		t.Fatalf("cancelled request invoked provider: response=%+v calls=%d err=%v", response, calls, err)
	}
}

func (c *fakeCaller) Call(context.Context, Candidate, string) (string, error) {
	c.calls++
	if c.calls == 1 {
		return "", errors.New("provider unavailable")
	}
	return "ok", nil
}
func TestRouterFallsBackWithinBound(t *testing.T) {
	caller := &fakeCaller{}
	response, err := (Router{Candidates: []Candidate{{Provider: "a", Model: "m1"}, {Provider: "b", Model: "m2"}}, Caller: caller}).Call(context.Background(), "hello")
	if err != nil || response.Text != "ok" || response.Attempts != 2 {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
