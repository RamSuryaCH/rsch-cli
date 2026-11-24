package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/viper"
)

func TestUntrustedWorkspaceConfigKeepsOnlySafePreferences(t *testing.T) {
	layer := map[string]any{
		"agents":        map[string]any{"coder": map[string]any{"model": "configured-model"}},
		"autoCompact":   false,
		"routing":       map[string]any{"coding": map[string]any{"models": []any{"configured-model"}}},
		"tui":           map[string]any{"theme": "gruvbox"},
		"wd":            "/",
		"data":          map[string]any{"directory": "/tmp/stolen"},
		"lsp":           map[string]any{"go": map[string]any{"command": "payload"}},
		"mcpServers":    map[string]any{"payload": map[string]any{"command": "payload"}},
		"hooks":         map[string]any{"command": "payload"},
		"shell":         map[string]any{"path": "/tmp/payload"},
		"autonomy":      "full",
		"policy":        map[string]any{"commandAllow": []any{"payload"}},
		"governance":    map[string]any{"approvedMCPServers": []any{"payload"}},
		"network":       map[string]any{"allowPrivate": true},
		"notifications": map[string]any{"command": "payload"},
	}

	filtered, blocked := filterWorkspaceConfig(layer, false)
	if got, want := filtered, map[string]any{
		"agents":      map[string]any{"coder": map[string]any{"model": "configured-model"}},
		"autoCompact": false,
		"routing":     map[string]any{"coding": map[string]any{"models": []any{"configured-model"}}},
		"tui":         map[string]any{"theme": "gruvbox"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered workspace config = %#v, want %#v", got, want)
	}
	if len(blocked) != len(layer)-4 {
		t.Fatalf("blocked settings = %v", blocked)
	}
}

func TestTrustedWorkspaceConfigKeepsProtectedSettings(t *testing.T) {
	layer := map[string]any{"wd": "/trusted", "lsp": map[string]any{"go": map[string]any{"command": "gopls"}}}
	filtered, blocked := filterWorkspaceConfig(layer, true)
	if !reflect.DeepEqual(filtered, layer) || len(blocked) != 0 {
		t.Fatalf("trusted layer changed: filtered=%#v blocked=%v", filtered, blocked)
	}
}

func TestWorkspaceTrustRequiresExactAbsoluteCanonicalPath(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	t.Setenv(trustedWorkspacesEnv, sibling)
	if isWorkspaceTrusted(root, nil) {
		t.Fatal("sibling trust entry trusted the workspace")
	}
	t.Setenv(trustedWorkspacesEnv, ".")
	if isWorkspaceTrusted(root, nil) {
		t.Fatal("relative trust entry trusted the workspace")
	}
	t.Setenv(trustedWorkspacesEnv, root)
	if !isWorkspaceTrusted(root, nil) {
		t.Fatal("exact absolute trust entry was not honored")
	}

	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(root, link); err == nil && !isWorkspaceTrusted(link, nil) {
		t.Fatal("canonical symlink target was not honored")
	}
}

func TestResolveTrustedDataDirectoryRejectsRepositoryRedirectAndSelfTrust(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	configHome := t.TempDir()
	outside := filepath.Join(t.TempDir(), "stolen-state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(trustedWorkspacesEnv, "")
	t.Setenv("RSCH_SESSION_CONFIG", "")
	t.Setenv("RSCH_COMMAND_CONFIG", "")
	writeConfigTestFile(t, filepath.Join(workspace, ".rsch", "config.json"), `{
		"trust":{"workspaces":[`+quotedJSONPath(workspace)+`]},
		"data":{"directory":`+quotedJSONPath(outside)+`}
	}`)

	got, err := ResolveTrustedDataDirectory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workspace, ".rsch")
	if got != want {
		t.Fatalf("untrusted repository redirected state to %q, want %q", got, want)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("resolver created or touched redirect target: %v", err)
	}
}

func TestResolveTrustedDataDirectoryHonorsUserTrustAndExplicitOverride(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	configHome := t.TempDir()
	trustedTarget := filepath.Join(t.TempDir(), "trusted-state")
	explicitTarget := filepath.Join(t.TempDir(), "explicit-state")
	explicitConfig := filepath.Join(t.TempDir(), "session.json")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(trustedWorkspacesEnv, "")
	t.Setenv("RSCH_SESSION_CONFIG", "")
	t.Setenv("RSCH_COMMAND_CONFIG", "")
	writeConfigTestFile(t, filepath.Join(home, ".rsch", "config.json"), `{"trust":{"workspaces":[`+quotedJSONPath(workspace)+`]}}`)
	writeConfigTestFile(t, filepath.Join(workspace, "rsch.json"), `{"data":{"directory":`+quotedJSONPath(trustedTarget)+`}}`)

	got, err := ResolveTrustedDataDirectory(workspace)
	if err != nil || got != trustedTarget {
		t.Fatalf("trusted workspace directory=%q err=%v", got, err)
	}

	writeConfigTestFile(t, explicitConfig, `{"data":{"directory":`+quotedJSONPath(explicitTarget)+`}}`)
	t.Setenv("RSCH_SESSION_CONFIG", explicitConfig)
	got, err = ResolveTrustedDataDirectory(workspace)
	if err != nil || got != explicitTarget {
		t.Fatalf("explicit directory=%q err=%v", got, err)
	}
}

func TestMergeLocalConfigProtectsTrustedLayersAndMergesSafeWorkspacePreferences(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(trustedWorkspacesEnv, "")
	t.Setenv("RSCH_SESSION_CONFIG", "")
	t.Setenv("RSCH_COMMAND_CONFIG", "")

	writeConfigTestFile(t, filepath.Join(home, ".rsch", "config.json"), `{
		"wd":"/trusted/root",
		"shell":{"path":"/trusted/shell"},
		"policy":{"productionDenied":true},
		"network":{"allowPrivate":false},
		"notifications":{"command":"trusted-notifier"}
	}`)
	writeConfigTestFile(t, filepath.Join(workspace, ".rsch", "config.json"), `{
		"trust":{"workspaces":[`+quotedJSONPath(workspace)+`]},
		"wd":"/",
		"shell":{"path":"/evil/shell"},
		"policy":{"productionDenied":false},
		"network":{"allowPrivate":true},
		"notifications":{"command":"payload"},
		"lsp":{"go":{"command":"payload"}},
		"mcpServers":{"payload":{"command":"payload"}},
		"tui":{"theme":"gruvbox"},
		"autoCompact":false
	}`)

	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults(false)
	trusted, err := mergeLocalConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Fatal("workspace configuration granted itself trust")
	}
	if got := viper.GetString("wd"); got != "/trusted/root" {
		t.Fatalf("working directory override = %q", got)
	}
	if got := viper.GetString("shell.path"); got != "/trusted/shell" {
		t.Fatalf("shell override = %q", got)
	}
	if !viper.GetBool("policy.productionDenied") || viper.GetBool("network.allowPrivate") {
		t.Fatalf("security policy was weakened: policy=%v network=%v", viper.GetBool("policy.productionDenied"), viper.GetBool("network.allowPrivate"))
	}
	if got := viper.GetString("notifications.command"); got != "trusted-notifier" {
		t.Fatalf("notification command override = %q", got)
	}
	if got := viper.GetString("tui.theme"); got != "gruvbox" || viper.GetBool("autoCompact") {
		t.Fatalf("safe preferences were not merged: theme=%q autoCompact=%v", got, viper.GetBool("autoCompact"))
	}
	if viper.IsSet("lsp.go.command") || viper.IsSet("mcpServers.payload.command") {
		t.Fatal("untrusted executable configuration was merged")
	}
}

func TestExplicitSessionLayerRemainsHigherPrecedenceThanUntrustedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	configHome := t.TempDir()
	session := filepath.Join(t.TempDir(), "session.json")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(trustedWorkspacesEnv, "")
	t.Setenv("RSCH_SESSION_CONFIG", session)
	t.Setenv("RSCH_COMMAND_CONFIG", "")

	writeConfigTestFile(t, filepath.Join(workspace, "rsch.json"), `{"shell":{"path":"/evil/shell"}}`)
	writeConfigTestFile(t, session, `{"shell":{"path":"/explicit/shell"},"policy":{"productionDenied":true}}`)

	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults(false)
	trusted, err := mergeLocalConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if trusted || viper.GetString("shell.path") != "/explicit/shell" || !viper.GetBool("policy.productionDenied") {
		t.Fatalf("explicit session layer lost precedence: trusted=%v shell=%q policy=%v", trusted, viper.GetString("shell.path"), viper.GetBool("policy.productionDenied"))
	}
}

func TestMergeLocalConfigHonorsTrustFromUserLayer(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(trustedWorkspacesEnv, "")
	t.Setenv("RSCH_SESSION_CONFIG", "")
	t.Setenv("RSCH_COMMAND_CONFIG", "")

	writeConfigTestFile(t, filepath.Join(home, ".rsch", "config.json"), `{"trust":{"workspaces":[`+quotedJSONPath(workspace)+`]}}`)
	writeConfigTestFile(t, filepath.Join(workspace, "rsch.json"), `{"wd":"/trusted-by-user","shell":{"path":"/workspace/shell"}}`)

	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults(false)
	trusted, err := mergeLocalConfig(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !trusted || viper.GetString("wd") != "/trusted-by-user" || viper.GetString("shell.path") != "/workspace/shell" {
		t.Fatalf("trusted workspace settings not merged: trusted=%v wd=%q shell=%q", trusted, viper.GetString("wd"), viper.GetString("shell.path"))
	}
}

func writeConfigTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func quotedJSONPath(path string) string {
	encoded, _ := json.Marshal(path)
	return string(encoded)
}
