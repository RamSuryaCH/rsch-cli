package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func schemaProperties(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	properties, ok := value["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no object properties: %#v", value)
	}
	return properties
}

func schemaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema value is not an object: %#v", value)
	}
	return object
}

func jsonFieldNames(t reflect.Type) map[string]struct{} {
	fields := make(map[string]struct{})
	for index := 0; index < t.NumField(); index++ {
		field := t.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if name != "-" {
			fields[name] = struct{}{}
		}
	}
	return fields
}

func TestGenerateSchemaMatchesConfigTopLevelKeys(t *testing.T) {
	schema := generateSchema()
	properties := schemaProperties(t, schema)
	want := jsonFieldNames(reflect.TypeOf(config.Config{}))

	for key := range want {
		if _, ok := properties[key]; !ok {
			t.Errorf("config key %q is missing from schema", key)
		}
	}
	for key := range properties {
		if _, ok := want[key]; !ok {
			t.Errorf("schema contains key %q that is not in config.Config", key)
		}
	}
}

func TestGenerateSchemaCoversContractFields(t *testing.T) {
	properties := schemaProperties(t, generateSchema())

	shell := schemaProperties(t, schemaObject(t, properties["shell"]))
	if schemaObject(t, shell["path"])["type"] != "string" {
		t.Error("shell.path should be a string")
	}
	if schemaObject(t, shell["args"])["type"] != "array" {
		t.Error("shell.args should be an array")
	}

	autonomy := schemaObject(t, properties["autonomy"])
	if autonomy["default"] != "assisted" {
		t.Errorf("autonomy default = %#v, want assisted", autonomy["default"])
	}
	if got := autonomy["enum"].([]string); len(got) != 3 {
		t.Fatalf("autonomy enum = %#v, want manual/assisted/autonomous", got)
	}

	governance := schemaProperties(t, schemaObject(t, properties["governance"]))
	for _, key := range []string{"approvedModels", "approvedPlugins", "approvedMCPServers", "sharedSkills", "sharedAgents", "requireVerification", "maxCost"} {
		if _, ok := governance[key]; !ok {
			t.Errorf("governance key %q is missing", key)
		}
	}

	lsp := schemaObject(t, properties["lsp"])["additionalProperties"].(map[string]any)
	lspProperties := schemaProperties(t, lsp)
	if schemaObject(t, lspProperties["disabled"])["type"] != "boolean" {
		t.Error("lsp.<language>.disabled should be a boolean")
	}
	if _, hasType := schemaObject(t, lspProperties["options"])["type"]; hasType {
		t.Error("lsp.<language>.options should accept any JSON value")
	}
	if got := lsp["anyOf"].([]map[string]any); len(got) != 2 {
		t.Fatalf("LSP conditional forms = %#v, want command or disabled", got)
	}

	mcp := schemaObject(t, properties["mcpServers"])["additionalProperties"].(map[string]any)
	mcpProperties := schemaProperties(t, mcp)
	for _, key := range []string{"command", "env", "args", "type", "url", "headers"} {
		if _, ok := mcpProperties[key]; !ok {
			t.Errorf("MCP server key %q is missing", key)
		}
	}
	variants := mcp["oneOf"].([]map[string]any)
	if len(variants) != 2 {
		t.Fatalf("MCP conditional forms = %#v, want stdio and SSE", variants)
	}
	if got := variants[0]["required"].([]string); len(got) != 1 || got[0] != "command" {
		t.Errorf("stdio MCP required = %#v, want command", got)
	}
	if got := variants[1]["required"].([]string); len(got) != 2 || got[0] != "type" || got[1] != "url" {
		t.Errorf("SSE MCP required = %#v, want type and url", got)
	}

	policy := schemaProperties(t, schemaObject(t, properties["policy"]))
	if schemaObject(t, policy["requireLSPIsolation"])["type"] != "boolean" {
		t.Error("policy.requireLSPIsolation should be a boolean")
	}

	providerProperties := schemaProperties(t, schemaObject(t, schemaObject(t, properties["providers"])["additionalProperties"]))
	if _, ok := providerProperties["provider"]; ok {
		t.Error("provider.provider is not a config field; provider names are map keys")
	}
}

func TestGeneratedSchemaIsJSON(t *testing.T) {
	if _, err := json.Marshal(generateSchema()); err != nil {
		t.Fatalf("generated schema is not JSON-serializable: %v", err)
	}
}

func TestGeneratedSchemaMatchesCommittedArtifact(t *testing.T) {
	generated, err := json.MarshalIndent(generateSchema(), "", "  ")
	if err != nil {
		t.Fatalf("marshal generated schema: %v", err)
	}
	generated = append(generated, '\n')

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate schema test file")
	}
	artifactPath := filepath.Join(filepath.Dir(testFile), "..", "..", "opencode-schema.json")
	committed, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read committed schema %q: %v", artifactPath, err)
	}

	if !bytes.Equal(generated, committed) {
		t.Fatalf("opencode-schema.json is stale; regenerate it with `go run ./cmd/schema > opencode-schema.json`")
	}
}
