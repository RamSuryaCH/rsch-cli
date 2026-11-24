package provider

import (
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

func TestGoogleProviderInitializationErrorsAreReturned(t *testing.T) {
	for _, name := range []string{"GOOGLE_API_KEY", "VERTEXAI_PROJECT", "VERTEXAI_LOCATION", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_REGION"} {
		t.Setenv(name, "")
	}
	for _, tc := range []struct {
		provider models.ModelProvider
		want     string
	}{
		{provider: models.ProviderGemini, want: "api key is required"},
		{provider: models.ProviderVertexAI, want: "project is required"},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			service, err := NewProvider(tc.provider)
			if service != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid configuration must fail construction, provider=%T err=%v", service, err)
			}
		})
	}
}
