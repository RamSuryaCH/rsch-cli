package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestCopilotTokenIgnoresOtherToolsStoredCredentials(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(CopilotTokenEnv, "")
	// A generic GitHub token is not consent to use Copilot.
	t.Setenv("GITHUB_TOKEN", "generic-github-token")
	dir := filepath.Join(configHome, "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored := []byte(`{"github.com":{"oauth_token":"stored-by-another-tool"}}`)
	for _, name := range []string{"hosts.json", "apps.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), stored, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if token, err := LoadGitHubToken(); err == nil || token != "" {
		t.Fatalf("LoadGitHubToken read a token it must ignore: %q", token)
	}
	if hasCopilotCredentials() {
		t.Fatal("stored Copilot files or GITHUB_TOKEN must not enable Copilot")
	}
}

func TestCopilotTokenUsesExplicitSourcesOnly(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv(CopilotTokenEnv, "explicit-token")
	if token, err := LoadGitHubToken(); err != nil || token != "explicit-token" {
		t.Fatalf("explicit token not used: %q %v", token, err)
	}
	t.Setenv(CopilotTokenEnv, "")
	viper.Set("providers.copilot.apiKey", "configured-token")
	if !hasCopilotCredentials() {
		t.Fatal("providers.copilot.apiKey should enable Copilot")
	}
}
