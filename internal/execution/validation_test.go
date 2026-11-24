package execution

import (
	"context"
	"errors"
	"testing"
)

func TestNewRejectsUnknownAutonomy(t *testing.T) {
	for _, autonomy := range []Autonomy{"invalid", "MANUAL", " manual "} {
		t.Run(string(autonomy), func(t *testing.T) {
			if _, err := New(Config{Policy: DefaultPolicy{}, Sandbox: fakeSandbox{}, Autonomy: autonomy}); err == nil {
				t.Fatal("unknown autonomy bypasses manual approval")
			}
		})
	}
}

func TestUnknownRiskFailsClosed(t *testing.T) {
	for _, value := range []Risk{"unknown", "HIGH", " high "} {
		t.Run(string(value), func(t *testing.T) {
			request := Request{Tool: Spec{Name: "write", Risk: value}}
			if got := (DefaultPolicy{}).Evaluate(request); got != Deny {
				t.Errorf("invalid risk decision=%s want=%s", got, Deny)
			}
			executor, err := New(Config{Policy: fixedPolicy(Allow), Sandbox: fakeSandbox{}, Autonomy: Autonomous})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = executor.ExecuteCallback(context.Background(), request, func(context.Context, Request) (string, error) {
				called = true
				return "", nil
			})
			if !errors.Is(err, ErrDenied) || called {
				t.Fatalf("invalid risk executed: err=%v called=%t", err, called)
			}
		})
	}
}
