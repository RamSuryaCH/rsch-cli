package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

func TestLoadHierarchyMergesNestedLayersInOrder(t *testing.T) {
	root := t.TempDir()
	paths := []string{filepath.Join(root, "global.json"), filepath.Join(root, "repository.json")}
	values := []map[string]any{
		{"agent": map[string]any{"model": "fast", "temperature": 0.2}, "enabled": true},
		{"agent": map[string]any{"model": "accurate"}, "enabled": false},
	}
	for index, path := range paths {
		data, err := json.Marshal(values[index])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := LoadHierarchy(paths...)
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := merged["agent"].(map[string]any)
	if !ok || agent["model"] != "accurate" || agent["temperature"] != 0.2 || merged["enabled"] != false {
		t.Fatalf("unexpected merged configuration: %#v", merged)
	}
}

func TestUpdateConfigFileTightensExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are platform-specific")
	}
	path := filepath.Join(t.TempDir(), "rsch.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := cfg
	cfg = &Config{}
	t.Cleanup(func() { cfg = previous })
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	if err := updateCfgFile(func(*Config) {}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config permissions = %o, want 600", got)
	}
}

func TestHierarchyPathsUsesStablePrecedenceOrder(t *testing.T) {
	paths := HierarchyPaths("/workspace/project")
	if len(paths) != 4 {
		t.Fatalf("expected four hierarchy paths, got %v", paths)
	}
	if filepath.Base(paths[0]) != "config.json" || filepath.Base(paths[1]) != "config.json" || paths[2] != filepath.Join("/workspace/project", ".rsch", "config.json") || paths[3] != filepath.Join("/workspace/project", "rsch.json") {
		t.Fatalf("unexpected hierarchy order: %v", paths)
	}
}

func TestValidateAgentRejectsUnsupportedModelWithoutDowngrade(t *testing.T) {
	cfg := &Config{Agents: map[AgentName]Agent{AgentCoder: {Model: models.ModelID("not-a-model")}}, Providers: map[models.ModelProvider]Provider{}}
	err := validateAgent(cfg, AgentCoder, cfg.Agents[AgentCoder])
	if err == nil || !strings.Contains(err.Error(), "unsupported model") {
		t.Fatalf("expected explicit model error, got %v", err)
	}
}

func TestValidateAgentRejectsMissingProviderWithoutDowngrade(t *testing.T) {
	model := models.SupportedModels[models.GPT41]
	cfg := &Config{Agents: map[AgentName]Agent{AgentCoder: {Model: model.ID}}, Providers: map[models.ModelProvider]Provider{}}
	err := validateAgent(cfg, AgentCoder, cfg.Agents[AgentCoder])
	if err == nil || !strings.Contains(err.Error(), "unconfigured provider") {
		t.Fatalf("expected explicit provider error, got %v", err)
	}
}

func TestValidateAgentRejectsInvalidFallbackModel(t *testing.T) {
	primary := models.SupportedModels[models.GPT41]
	cfg := &Config{
		Agents:    map[AgentName]Agent{AgentCoder: {Model: primary.ID, FallbackModels: []models.ModelID{"not-a-model"}}},
		Providers: map[models.ModelProvider]Provider{primary.Provider: {APIKey: "configured"}},
	}
	err := validateAgent(cfg, AgentCoder, cfg.Agents[AgentCoder])
	if err == nil || !strings.Contains(err.Error(), "unsupported fallback model") {
		t.Fatalf("expected explicit fallback model error, got %v", err)
	}
}

func TestValidateAgentRejectsCredentiallessFallbackProvider(t *testing.T) {
	primary := models.SupportedModels[models.GPT41]
	fallbackID := models.Claude4Sonnet
	fallback := models.SupportedModels[fallbackID]
	cfg := &Config{
		Agents: map[AgentName]Agent{AgentCoder: {Model: primary.ID, FallbackModels: []models.ModelID{fallbackID}}},
		Providers: map[models.ModelProvider]Provider{
			primary.Provider:  {APIKey: "configured"},
			fallback.Provider: {},
		},
	}
	err := validateAgent(cfg, AgentCoder, cfg.Agents[AgentCoder])
	if err == nil || !strings.Contains(err.Error(), "credential-less provider") {
		t.Fatalf("expected explicit fallback provider error, got %v", err)
	}
}

func TestValidateRoutingRejectsInvalidClassAndDuplicates(t *testing.T) {
	model := models.SupportedModels[models.GPT41]
	base := &Config{Providers: map[models.ModelProvider]Provider{model.Provider: {APIKey: "configured"}}}
	base.Routing = map[string]RoutingRule{"unknown": {Models: []models.ModelID{model.ID}}}
	if err := validateRouting(base); err == nil || !strings.Contains(err.Error(), "unsupported task class") {
		t.Fatalf("expected invalid routing class error, got %v", err)
	}
	base.Routing = map[string]RoutingRule{"coding": {Models: []models.ModelID{model.ID, model.ID}}}
	if err := validateRouting(base); err == nil || !strings.Contains(err.Error(), "repeats model") {
		t.Fatalf("expected duplicate routing model error, got %v", err)
	}
}

func TestValidateRoutingAcceptsConfiguredCandidates(t *testing.T) {
	model := models.SupportedModels[models.GPT41]
	cfg := &Config{
		Providers: map[models.ModelProvider]Provider{model.Provider: {APIKey: "configured"}},
		Routing:   map[string]RoutingRule{"coding": {Models: []models.ModelID{model.ID}, MaxCostPerMillion: 20, MaxLatencyMS: 5000}},
	}
	if err := validateRouting(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestAzureEntraCredentialsAreAcceptedAcrossValidationPaths(t *testing.T) {
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://example.openai.azure.com")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2025-04-01-preview")
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	azureModel := models.SupportedModels[models.AzureGPT41]
	if mode := configuredAzureCredentialMode(Provider{}); mode != azureCredentialDefault {
		t.Fatalf("keyless Azure auth mode = %q, want %q", mode, azureCredentialDefault)
	}
	if mode := configuredAzureCredentialMode(Provider{APIKey: "configured-key"}); mode != azureCredentialAPIKey {
		t.Fatalf("keyed Azure auth mode = %q, want %q", mode, azureCredentialAPIKey)
	}

	t.Run("primary agent", func(t *testing.T) {
		config := &Config{
			Agents:    map[AgentName]Agent{AgentCoder: {Model: azureModel.ID, MaxTokens: 1024}},
			Providers: map[models.ModelProvider]Provider{models.ProviderAzure: {}},
		}
		if err := validateAgent(config, AgentCoder, config.Agents[AgentCoder]); err != nil {
			t.Fatalf("keyless Azure primary rejected: %v", err)
		}
		if got := config.Providers[models.ProviderAzure].APIKey; got != "" {
			t.Fatalf("Entra validation stored synthetic API key %q", got)
		}
	})

	t.Run("fallback agent", func(t *testing.T) {
		primary := models.SupportedModels[models.GPT41]
		config := &Config{
			Agents: map[AgentName]Agent{AgentCoder: {
				Model:          primary.ID,
				FallbackModels: []models.ModelID{azureModel.ID},
				MaxTokens:      1024,
			}},
			Providers: map[models.ModelProvider]Provider{
				primary.Provider:     {APIKey: "configured"},
				models.ProviderAzure: {},
			},
		}
		if err := validateAgent(config, AgentCoder, config.Agents[AgentCoder]); err != nil {
			t.Fatalf("keyless Azure fallback rejected: %v", err)
		}
	})

	t.Run("routing", func(t *testing.T) {
		config := &Config{
			Providers: map[models.ModelProvider]Provider{models.ProviderAzure: {}},
			Routing:   map[string]RoutingRule{"coding": {Models: []models.ModelID{azureModel.ID}}},
		}
		if err := validateRouting(config); err != nil {
			t.Fatalf("keyless Azure route rejected: %v", err)
		}
	})

	t.Run("provider loop", func(t *testing.T) {
		previous := cfg
		defer func() { cfg = previous }()
		cfg = &Config{
			Autonomy:  "manual",
			Providers: map[models.ModelProvider]Provider{models.ProviderAzure: {}},
			Resources: ResourceLimits{MaxAgents: 1, MaxConcurrentTools: 1, MaxModelRequests: 1, MaxWorktrees: 1, MaxBackgroundJobs: 1},
		}
		if err := Validate(); err != nil {
			t.Fatalf("keyless Azure provider rejected: %v", err)
		}
	})
}

func TestAzureCredentialsRequireCompleteServiceConfiguration(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "configured-key")
	tests := []struct {
		name       string
		endpoint   string
		apiVersion string
	}{
		{name: "both missing"},
		{name: "endpoint missing", apiVersion: "2025-04-01-preview"},
		{name: "API version missing", endpoint: "https://example.openai.azure.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AZURE_OPENAI_ENDPOINT", test.endpoint)
			t.Setenv("AZURE_OPENAI_API_VERSION", test.apiVersion)
			providerCfg := Provider{APIKey: "configured-key"}
			if mode := configuredAzureCredentialMode(providerCfg); mode != azureCredentialUnavailable {
				t.Fatalf("incomplete Azure auth mode = %q, want unavailable", mode)
			}
			if providerCredentialsAvailable(models.ProviderAzure, providerCfg) {
				t.Fatal("incomplete Azure configuration reported usable credentials")
			}
			azureModel := models.SupportedModels[models.AzureGPT41]
			config := &Config{
				Agents:    map[AgentName]Agent{AgentCoder: {Model: azureModel.ID, MaxTokens: 1024}},
				Providers: map[models.ModelProvider]Provider{models.ProviderAzure: providerCfg},
			}
			if err := validateAgent(config, AgentCoder, config.Agents[AgentCoder]); err == nil || !strings.Contains(err.Error(), "credential-less provider") {
				t.Fatalf("expected incomplete Azure agent configuration error, got %v", err)
			}
		})
	}
}

func TestValidateRejectsNegativeBudgets(t *testing.T) {
	previous := cfg
	defer func() { cfg = previous }()
	cfg = &Config{Autonomy: "manual", Budget: BudgetConfig{Task: -1}}
	if err := Validate(); err == nil || !strings.Contains(err.Error(), "budget.task") {
		t.Fatalf("expected negative task budget validation error, got %v", err)
	}
}
