package config

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

// fakeOllama serves an OpenAI-compatible model list the way Ollama does,
// with an embedding model listed first.
func fakeOllama(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"nomic-embed-text:latest","object":"model"},{"id":"llama3.2:3b","object":"model"}]}`))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

// closedEndpoint returns an endpoint on a port nothing listens on.
func closedEndpoint(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return "http://" + address + "/v1"
}

func setupLocalDetect(t *testing.T, enabled bool, candidates ...string) string {
	t.Helper()
	path := setupConfigLoad(t)
	t.Setenv("LOCAL_ENDPOINT", "")
	t.Setenv(LocalDetectDisableEnv, "")
	t.Setenv(LocalCandidatesEnv, strings.Join(candidates, ","))

	previousDetect := autoDetectLocal
	autoDetectLocal = func() bool { return enabled && os.Getenv(LocalDetectDisableEnv) == "" }
	previousEndpoint, previousSource := models.LocalEndpoint, models.LocalEndpointSource
	previousDefault := models.DefaultLocalModelID
	previousPopularity, hadPopularity := models.ProviderPopularity[models.ProviderLocal]
	before := map[models.ModelID]bool{}
	for id := range models.SupportedModels {
		before[id] = true
	}
	models.LocalEndpoint, models.LocalEndpointSource = "", ""
	t.Cleanup(func() {
		autoDetectLocal = previousDetect
		models.LocalEndpoint, models.LocalEndpointSource = previousEndpoint, previousSource
		models.DefaultLocalModelID = previousDefault
		if hadPopularity {
			models.ProviderPopularity[models.ProviderLocal] = previousPopularity
		} else {
			delete(models.ProviderPopularity, models.ProviderLocal)
		}
		for id := range models.SupportedModels {
			if !before[id] {
				delete(models.SupportedModels, id)
			}
		}
	})
	return path
}

func TestLoadAutoDetectsLocalOllamaWithoutConfig(t *testing.T) {
	endpoint := fakeOllama(t)
	setupLocalDetect(t, true, closedEndpoint(t), endpoint)

	loaded, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Agents[AgentCoder].Model; got != "local.llama3.2:3b" {
		t.Fatalf("coder model = %q, want the detected chat model local.llama3.2:3b", got)
	}
	if models.LocalEndpoint != endpoint {
		t.Fatalf("local endpoint = %q, want %q", models.LocalEndpoint, endpoint)
	}
	detail, ok := ActiveProvider(loaded)
	if !ok || !strings.Contains(detail, "local (auto-detected "+endpoint+")") || !strings.Contains(detail, "llama3.2:3b") {
		t.Fatalf("ActiveProvider = %q, %v", detail, ok)
	}
}

func TestLoadExplicitModelSkipsLocalDetection(t *testing.T) {
	endpoint := fakeOllama(t)
	path := setupLocalDetect(t, true, endpoint)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	if err := os.WriteFile(path, []byte(`{"agents":{"coder":{"model":"claude-4-sonnet"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Agents[AgentCoder].Model; got != models.Claude4Sonnet {
		t.Fatalf("coder model = %q, want explicit %q", got, models.Claude4Sonnet)
	}
	if models.LocalEndpoint != "" {
		t.Fatalf("explicit model should not probe local servers, got endpoint %q", models.LocalEndpoint)
	}
	if detail, ok := ActiveProvider(loaded); !ok || !strings.HasPrefix(detail, "hosted anthropic") {
		t.Fatalf("ActiveProvider = %q, %v", detail, ok)
	}
}

func TestLoadFallsBackToHostedWhenNoLocalServer(t *testing.T) {
	setupLocalDetect(t, true, closedEndpoint(t))
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	loaded, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	model := models.SupportedModels[loaded.Agents[AgentCoder].Model]
	if model.Provider != models.ProviderAnthropic {
		t.Fatalf("coder provider = %q, want hosted anthropic fallback", model.Provider)
	}
	if models.LocalEndpoint != "" {
		t.Fatalf("unexpected local endpoint %q", models.LocalEndpoint)
	}
}

func TestLoadDetectionCanBeDisabled(t *testing.T) {
	endpoint := fakeOllama(t)
	setupLocalDetect(t, true, endpoint)
	t.Setenv(LocalDetectDisableEnv, "1")

	loaded, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if models.LocalEndpoint != "" {
		t.Fatalf("%s should disable probing, got endpoint %q", LocalDetectDisableEnv, models.LocalEndpoint)
	}
	if detail, ok := ActiveProvider(loaded); ok || !strings.Contains(detail, "no provider configured") {
		t.Fatalf("ActiveProvider = %q, %v; want a clear no-provider message", detail, ok)
	}
}

func TestLoadPrefersDetectedLocalOverHostedKeys(t *testing.T) {
	endpoint := fakeOllama(t)
	setupLocalDetect(t, true, endpoint)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	loaded, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Agents[AgentCoder].Model; got != "local.llama3.2:3b" {
		t.Fatalf("coder model = %q; a reachable local server should win when no model is configured", got)
	}
}
