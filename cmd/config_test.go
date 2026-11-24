package cmd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRedactConfigProtectsMCPHeadersAndEnvironment(t *testing.T) {
	fixture := `{
		"mcpServers": {
			"remote": {
				"url": "https://example.test/mcp",
				"headers": {
					"Authorization": "Bearer header-value",
					"pRoXy-AuThOrIzAtIoN": "Basic proxy-value",
					"Cookie": "session=cookie-value",
					"X-API-Key": "header-key-value",
					"Accept": "text/event-stream"
				}
			},
			"local": {
				"command": "example-mcp",
				"env": ["API_KEY=env-key-value", "CUSTOM_LOGIN=arbitrary-value==", "INHERITED_VAR"],
				"args": ["--stdio"]
			}
		},
		"providers": {"example": {"apiKey": "provider-key-value"}},
		"resources": {"maxAgents": 4}
	}`
	var value map[string]any
	if err := json.Unmarshal([]byte(fixture), &value); err != nil {
		t.Fatal(err)
	}
	redactConfig(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"header-value", "proxy-value", "cookie-value", "header-key-value", "env-key-value", "arbitrary-value", "provider-key-value"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("configuration output contains credential fixture %q", secret)
		}
	}
	servers := value["mcpServers"].(map[string]any)
	remote := servers["remote"].(map[string]any)
	headers := remote["headers"].(map[string]any)
	if headers["Accept"] != "text/event-stream" || remote["url"] != "https://example.test/mcp" {
		t.Fatal("non-sensitive MCP connection settings were changed")
	}
	local := servers["local"].(map[string]any)
	wantEnv := []any{"API_KEY=[redacted]", "CUSTOM_LOGIN=[redacted]", "INHERITED_VAR"}
	if !reflect.DeepEqual(local["env"], wantEnv) {
		t.Fatalf("environment = %v, want names preserved and assigned values redacted", local["env"])
	}
	if local["command"] != "example-mcp" || !reflect.DeepEqual(local["args"], []any{"--stdio"}) {
		t.Fatal("non-sensitive MCP launch settings were changed")
	}
	if value["resources"].(map[string]any)["maxAgents"] != float64(4) {
		t.Fatal("resource settings were changed")
	}
	redactConfig(value)
	secondPass, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(secondPass) {
		t.Fatal("configuration redaction must be idempotent")
	}
}

func TestRedactConfigEnvironmentSupportsTypedAndMapValues(t *testing.T) {
	for _, environment := range []any{
		[]string{"CUSTOM_VALUE=typed-value"},
		map[string]any{"CUSTOM_VALUE": "mapped-value"},
	} {
		value := map[string]any{"env": environment}
		redactConfig(value)
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "typed-value") || strings.Contains(string(encoded), "mapped-value") {
			t.Fatalf("environment assignment remained unredacted: %s", encoded)
		}
	}
}

func TestRedactConfigUsesSharedTextRedactionForNestedValues(t *testing.T) {
	value := map[string]any{
		"notes": []any{map[string]any{"description": "Authorization: Bearer nested-value"}, "Authorization: Bearer array-value"},
	}
	redactConfig(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "nested-value") || strings.Contains(string(encoded), "array-value") {
		t.Fatalf("recognizable credential remained unredacted: %s", encoded)
	}
}
