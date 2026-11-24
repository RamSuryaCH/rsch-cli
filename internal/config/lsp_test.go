package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLSPConfigUsesDisabledFieldContract(t *testing.T) {
	data, err := json.Marshal(LSPConfig{Disabled: true, Command: "gopls"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if !strings.Contains(encoded, `"disabled":true`) || strings.Contains(encoded, `"enabled"`) {
		t.Fatalf("unexpected LSP config encoding: %s", encoded)
	}

	var decoded LSPConfig
	if err := json.Unmarshal([]byte(`{"disabled":true,"command":"gopls"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Disabled {
		t.Fatal("disabled LSP entry was not decoded")
	}
}
