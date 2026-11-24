package app

import (
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/execution"
)

func TestConfiguredExecutionPolicyPreservesExplicitExecutablePaths(t *testing.T) {
	policy := configuredExecutionPolicy(config.PolicyConfig{CommandAllow: []string{" /trusted/bin/check ", "go", " "}})
	for _, test := range []struct {
		command string
		want    execution.Decision
	}{
		{command: "/trusted/bin/check", want: execution.Allow},
		{command: "check", want: execution.Deny},
		{command: "/workspace/bin/check", want: execution.Deny},
		{command: "go", want: execution.Allow},
		{command: "/workspace/go", want: execution.Deny},
	} {
		t.Run(test.command, func(t *testing.T) {
			if got := policy.Evaluate(execution.Request{Command: []string{test.command}}); got != test.want {
				t.Fatalf("decision = %s, want %s", got, test.want)
			}
		})
	}
}
