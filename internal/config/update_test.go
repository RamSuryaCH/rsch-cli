package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/spf13/viper"
)

func setupModelUpdate(t *testing.T) string {
	t.Helper()
	previous := cfg
	t.Cleanup(func() { cfg = previous })
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "rsch.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	cfg = &Config{
		Agents:    map[AgentName]Agent{AgentCoder: {Model: models.GPT41, MaxTokens: 1024, TimeoutMS: 12345, ReasoningEffort: "high"}},
		Providers: map[models.ModelProvider]Provider{models.ProviderOpenAI: {APIKey: "configured"}},
	}
	return path
}

func TestUpdateAgentModelEnforcesGovernance(t *testing.T) {
	path := setupModelUpdate(t)
	cfg.Governance.ApprovedModels = []string{string(models.GPT41)}
	previous := cfg.Agents[AgentCoder]
	if err := UpdateAgentModel(AgentCoder, models.GPT41Mini); err == nil || !strings.Contains(err.Error(), "governance") {
		t.Fatalf("expected governance denial, got %v", err)
	}
	if !reflect.DeepEqual(cfg.Agents[AgentCoder], previous) {
		t.Fatal("rejected model changed active configuration")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{}` {
		t.Fatalf("rejected model changed persisted configuration: %q, %v", data, err)
	}
}

func TestUpdateAgentModelPreservesTimeoutAndPersistsValidatedSettings(t *testing.T) {
	path := setupModelUpdate(t)
	if err := UpdateAgentModel(AgentCoder, models.GPT41Mini); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	active := cfg.Agents[AgentCoder]
	if active.TimeoutMS != 12345 || active.ReasoningEffort != "" || !reflect.DeepEqual(active, persisted.Agents[AgentCoder]) {
		t.Fatalf("active and persisted settings differ or lost timeout: active=%+v persisted=%+v", active, persisted.Agents[AgentCoder])
	}
}

func TestUpdateAgentModelPersistenceFailurePreservesActiveSettings(t *testing.T) {
	path := setupModelUpdate(t)
	previous := cfg.Agents[AgentCoder]
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAgentModel(AgentCoder, models.GPT41Mini); err == nil {
		t.Fatal("expected persistence error")
	}
	if !reflect.DeepEqual(cfg.Agents[AgentCoder], previous) {
		t.Fatalf("failed update changed active configuration: %+v", cfg.Agents[AgentCoder])
	}
}

func TestUpdateThemePreservesUnrelatedConfiguration(t *testing.T) {
	path := setupModelUpdate(t)
	contents := `{"trust":{"workspaces":["/trusted/project"]},"autoCompact":false,"future":9007199254740993,"TUI":{"theme":"old","custom":true},"agents":{"coder":{"model":"gpt-4.1","maxTokens":1024,"custom":42}}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTheme("new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	decoder = json.NewDecoder(strings.NewReader(strings.Replace(contents, `"old"`, `"new"`, 1)))
	decoder.UseNumber()
	if err := decoder.Decode(&want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("theme update changed unrelated settings: got %#v, want %#v", got, want)
	}
}

func TestUpdateConfigRejectsNullDocument(t *testing.T) {
	path := setupModelUpdate(t)
	if err := os.WriteFile(path, []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := updateCfgFile(func(*Config) { called = true }); err == nil || called {
		t.Fatalf("null document reached update callback: called=%v err=%v", called, err)
	}
}

func TestUpdateThemePersistenceFailurePreservesActiveSettings(t *testing.T) {
	path := setupModelUpdate(t)
	cfg.TUI.Theme = "old"
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTheme("new"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if cfg.TUI.Theme != "old" {
		t.Fatal("failed persistence changed active theme")
	}
}

func TestUpdateAgentModelPreservesUnknownAgentFields(t *testing.T) {
	path := setupModelUpdate(t)
	if err := os.WriteFile(path, []byte(`{"agents":{"coder":{"model":"gpt-4.1","custom":9007199254740993}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAgentModel(AgentCoder, models.GPT41Mini); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"custom": 9007199254740993`) || !strings.Contains(string(data), `"model": "gpt-4.1-mini"`) {
		t.Fatalf("model update lost unknown agent fields: %s", data)
	}
}

func TestRemoveMCPServerPreservesOtherServersAndTrust(t *testing.T) {
	path := setupModelUpdate(t)
	contents := `{"mcpServers":{"remove":{"command":"old"},"keep":{"command":"safe","future":true}},"trust":{"workspaces":["/trusted"]}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.MCPServers = map[string]MCPServer{"remove": {Command: "old"}, "keep": {Command: "safe"}}
	if err := RemoveMCPServer("remove"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	servers := persisted["mcpServers"].(map[string]any)
	if _, exists := servers["remove"]; exists || len(servers) != 1 || servers["keep"].(map[string]any)["future"] != true || persisted["trust"] == nil {
		t.Fatalf("server removal changed unrelated configuration: %s", data)
	}
}

func TestConfigReplacementDoesNotTruncateExistingHardLink(t *testing.T) {
	path := setupModelUpdate(t)
	original := filepath.Join(filepath.Dir(path), "original.json")
	if err := os.Link(path, original); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := UpdateTheme("new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(original)
	if err != nil || string(data) != `{}` {
		t.Fatalf("configuration update truncated the existing file: %q, %v", data, err)
	}
}
