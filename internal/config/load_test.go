package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/spf13/viper"
)

func setupConfigLoad(t *testing.T) string {
	t.Helper()
	previous := cfg
	cfg = nil
	t.Cleanup(func() { cfg = previous })
	logger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(logger) })
	viper.Reset()
	t.Cleanup(viper.Reset)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("RSCH_SESSION_CONFIG", "")
	t.Setenv("RSCH_COMMAND_CONFIG", "")
	t.Setenv("RSCH_DEV_DEBUG", "")
	t.Setenv("OPENCODE_DEV_DEBUG", "")
	for _, name := range []string{
		"GITHUB_TOKEN", "RSCH_COPILOT_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GROQ_API_KEY", "OPENROUTER_API_KEY", "XAI_API_KEY",
		"AZURE_OPENAI_ENDPOINT", "AZURE_OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE", "AWS_DEFAULT_PROFILE",
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"VERTEXAI_PROJECT", "VERTEXAI_LOCATION", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_REGION", "GOOGLE_CLOUD_LOCATION",
	} {
		t.Setenv(name, "")
	}
	return filepath.Join(home, "rsch.json")
}

func TestLoadFailureDoesNotCacheInvalidConfiguration(t *testing.T) {
	path := setupConfigLoad(t)
	if err := os.WriteFile(path, []byte(`{"autonomy":"invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := Load(root, false); err == nil || !strings.Contains(err.Error(), "autonomy") {
			t.Fatalf("load attempt %d unexpectedly accepted invalid config: %v", attempt, err)
		}
		if Get() != nil {
			t.Errorf("load attempt %d published invalid configuration", attempt)
		}
	}
}

func TestLoadRetryPreservesValidatedTitleSettings(t *testing.T) {
	path := setupConfigLoad(t)
	root := t.TempDir()
	if err := os.WriteFile(path, []byte(`invalid JSON`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, false); err == nil {
		t.Fatal("expected invalid configuration error")
	}
	if err := os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"configured"}},"agents":{"title":{"model":"gpt-4.1","timeoutMs":12345}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if title := loaded.Agents[AgentTitle]; title.MaxTokens != 80 || title.TimeoutMS != 12345 {
		t.Fatalf("load lost title settings: %+v", title)
	}
	if Get() != loaded {
		t.Fatal("successful load did not publish validated configuration")
	}
}

func TestLoadWithoutProvidersDoesNotIntroduceInvalidAgent(t *testing.T) {
	path := setupConfigLoad(t)
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	if err := Validate(); err != nil {
		t.Fatalf("load published invalid agent after validation: %v", err)
	}
}

func TestLoadDefaultsDoNotLogWarnings(t *testing.T) {
	path := setupConfigLoad(t)
	if err := os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"configured"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	loaded, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("ordinary configuration defaults emitted diagnostics: %s", output.String())
	}
	for name, agent := range loaded.Agents {
		if agent.MaxTokens <= 0 {
			t.Errorf("agent %s did not receive its default token limit", name)
		}
	}
}

func TestLoadRejectsNegativeTokenLimit(t *testing.T) {
	path := setupConfigLoad(t)
	if err := os.WriteFile(path, []byte(`{"providers":{"openai":{"apiKey":"configured"}},"agents":{"coder":{"maxTokens":-1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("negative token limit was not rejected: %v", err)
	}
}

func TestDebugLoggerRedactsCredentialsBeforeWritingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the process-lifetime debug log handle prevents temporary-directory cleanup on Windows")
	}
	path := setupConfigLoad(t)
	dataDir := filepath.Join(t.TempDir(), "state")
	previousMessageDir := logging.MessageDir
	t.Cleanup(func() { logging.MessageDir = previousMessageDir })
	t.Setenv("RSCH_DEV_DEBUG", "true")
	if err := os.WriteFile(path, []byte(`{"data":{"directory":`+quotedJSONPath(dataDir)+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	slog.Info(`request failed password="secret-message-value"`, "api_key", "secret-api-value",
		slog.Group("request", "Authorization", "Basic secret-header-value", "status", "retrying"))
	data, err := os.ReadFile(filepath.Join(dataDir, "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-message-value", "secret-api-value", "secret-header-value"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("debug log exposed fixture credential %q", secret)
		}
	}
	if !strings.Contains(string(data), "[REDACTED]") || !strings.Contains(string(data), "retrying") {
		t.Fatal("debug log did not preserve useful fields with redaction")
	}
}
