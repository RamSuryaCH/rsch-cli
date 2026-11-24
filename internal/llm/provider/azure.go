package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/azure"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type azureAuthMode string

const (
	azureAuthAPIKey            azureAuthMode = "api-key"
	azureAuthDefaultCredential azureAuthMode = "default-credential"
)

type azureClient struct {
	*openaiClient
	authMode azureAuthMode
	initErr  error
}

type AzureClient ProviderClient

func newAzureClient(opts providerClientOptions) AzureClient {
	endpoint := strings.TrimSpace(os.Getenv("AZURE_OPENAI_ENDPOINT"))      // ex: https://foo.openai.azure.com
	apiVersion := strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_VERSION")) // ex: 2025-04-01-preview
	client := &azureClient{}
	if endpoint == "" || apiVersion == "" {
		client.initErr = errors.New("azure provider requires AZURE_OPENAI_ENDPOINT and AZURE_OPENAI_API_VERSION")
		return client
	}

	reqOpts := append(withoutOpenAICredentials(),
		azure.WithEndpoint(endpoint, apiVersion),
	)

	key := strings.TrimSpace(opts.apiKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_KEY"))
	}
	if key != "" {
		client.authMode = azureAuthAPIKey
		reqOpts = append(reqOpts, azure.WithAPIKey(key))
	} else if cred, err := azidentity.NewDefaultAzureCredential(nil); err == nil {
		client.authMode = azureAuthDefaultCredential
		reqOpts = append(reqOpts, azure.WithTokenCredential(cred))
	} else {
		client.initErr = fmt.Errorf("initialize Azure default credential: %w", err)
		return client
	}

	client.openaiClient = &openaiClient{
		providerOptions: opts,
		client:          openai.NewClient(reqOpts...),
	}
	return client
}

func (a *azureClient) send(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) (*ProviderResponse, error) {
	if a == nil || a.initErr != nil {
		if a == nil {
			return nil, errors.New("azure provider is not initialized")
		}
		return nil, a.initErr
	}
	return a.openaiClient.send(ctx, messages, toolset)
}

func (a *azureClient) stream(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) <-chan ProviderEvent {
	if a != nil && a.initErr == nil {
		return a.openaiClient.stream(ctx, messages, toolset)
	}
	events := make(chan ProviderEvent, 1)
	err := errors.New("azure provider is not initialized")
	if a != nil {
		err = a.initErr
	}
	events <- ProviderEvent{Type: EventError, Error: err}
	close(events)
	return events
}
