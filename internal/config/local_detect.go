package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

// LocalDetectDisableEnv turns off automatic detection of a local model server.
const LocalDetectDisableEnv = "RSCH_NO_LOCAL_DETECT"

// LocalCandidatesEnv overrides the comma-separated endpoints probed for a
// local OpenAI-compatible server (default: Ollama, then LM Studio).
const LocalCandidatesEnv = "RSCH_LOCAL_CANDIDATES"

const localProbeTimeout = 750 * time.Millisecond

// autoDetectLocal reports whether Load may probe for a local model server.
// Package tests elsewhere in the repository load configuration on machines
// that may run Ollama, so probing is off under `go test` unless a test
// replaces this function.
var autoDetectLocal = func() bool {
	if strings.TrimSpace(os.Getenv(LocalDetectDisableEnv)) != "" {
		return false
	}
	return !testing.Testing()
}

func localCandidates() []string {
	if raw := strings.TrimSpace(os.Getenv(LocalCandidatesEnv)); raw != "" {
		var candidates []string
		for _, candidate := range strings.Split(raw, ",") {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				candidates = append(candidates, candidate)
			}
		}
		return candidates
	}
	return models.DefaultLocalCandidates
}

// detectLocalProvider makes a reachable local server the default when the
// user has not chosen a model explicitly. Hosted providers are then used only
// when explicitly configured or when no local server answers.
func detectLocalProvider() {
	if strings.TrimSpace(os.Getenv("LOCAL_ENDPOINT")) != "" || models.LocalEndpoint != "" {
		return
	}
	if viper.IsSet("agents.coder.model") || !autoDetectLocal() {
		return
	}
	models.DetectLocal(localCandidates(), localProbeTimeout)
}

// ActiveProvider describes the provider and model the coder agent will use,
// for user-facing notices and `rsch doctor`.
func ActiveProvider(current *Config) (string, bool) {
	if current == nil {
		return "no configuration loaded", false
	}
	agent, ok := current.Agents[AgentCoder]
	if !ok || agent.Model == "" {
		return "no provider configured; start Ollama (http://localhost:11434) or set LOCAL_ENDPOINT or a provider API key", false
	}
	model, ok := models.SupportedModels[agent.Model]
	if !ok {
		return fmt.Sprintf("model %s is not supported", agent.Model), false
	}
	if model.Provider == models.ProviderLocal {
		source := models.LocalEndpointSource
		if source == "" {
			source = "configured"
		}
		return fmt.Sprintf("local (%s %s), model %s", source, models.LocalEndpoint, model.APIModel), true
	}
	return fmt.Sprintf("hosted %s, model %s", model.Provider, model.APIModel), true
}
