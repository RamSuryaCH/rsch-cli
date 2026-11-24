package models

import "testing"

func TestConvertLocalModelUsesAdvertisedContextLimits(t *testing.T) {
	tests := []struct {
		name   string
		loaded int64
		max    int64
		want   int64
	}{
		{name: "loaded context takes precedence", loaded: 8_192, max: 32_768, want: 8_192},
		{name: "advertised context for unloaded model", max: 32_768, want: 32_768},
		{name: "advertised small context", max: 2_048, want: 2_048},
		{name: "invalid loaded context", loaded: -1, max: 8_192, want: 8_192},
		{name: "missing context uses default", want: 4_096},
		{name: "invalid context uses default", loaded: -1, max: -1, want: 4_096},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := convertLocalModel(localModel{ID: "local-model", LoadedContextLength: test.loaded, MaxContextLength: test.max})
			if model.ContextWindow != test.want || model.DefaultMaxTokens != test.want {
				t.Fatalf("context=%d output=%d, want %d", model.ContextWindow, model.DefaultMaxTokens, test.want)
			}
		})
	}
}

func TestConvertLocalModelReasoningIsOptIn(t *testing.T) {
	t.Setenv("LOCAL_ENDPOINT_REASONING", "")
	if convertLocalModel(localModel{ID: "llama3.2:3b"}).CanReason {
		t.Fatal("local models must not send reasoning parameters unless explicitly enabled")
	}
	t.Setenv("LOCAL_ENDPOINT_REASONING", "true")
	if !convertLocalModel(localModel{ID: "qwen3:8b"}).CanReason {
		t.Fatal("LOCAL_ENDPOINT_REASONING=true should enable reasoning for local models")
	}
}

func TestLoadLocalModelsRecordsDefaultModel(t *testing.T) {
	previous := DefaultLocalModelID
	t.Cleanup(func() { DefaultLocalModelID = previous })
	DefaultLocalModelID = ""
	loadLocalModels([]localModel{{ID: "first"}, {ID: "second"}})
	if DefaultLocalModelID != ModelID("local.first") {
		t.Fatalf("default local model = %q, want local.first", DefaultLocalModelID)
	}
}
