package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/network"
	"github.com/spf13/viper"
)

const (
	ProviderLocal ModelProvider = "local"

	localModelsPath        = "v1/models"
	lmStudioBetaModelsPath = "api/v0/models"
)

// LocalEndpoint is the OpenAI-compatible base URL of the local model server
// in use, either LOCAL_ENDPOINT or one found by DetectLocal. Empty when no
// local server is in use.
var LocalEndpoint string

// LocalEndpointSource describes how LocalEndpoint was chosen: "LOCAL_ENDPOINT"
// or "auto-detected".
var LocalEndpointSource string

// DefaultLocalCandidates are the OpenAI-compatible endpoints probed when no
// provider is configured: Ollama, then LM Studio, at their default ports.
var DefaultLocalCandidates = []string{
	"http://localhost:11434/v1",
	"http://localhost:1234/v1",
}

func init() {
	if endpoint := os.Getenv("LOCAL_ENDPOINT"); endpoint != "" {
		if DiscoverLocal(endpoint, 10*time.Second) {
			LocalEndpointSource = "LOCAL_ENDPOINT"
		}
	}
}

// DiscoverLocal lists the chat models served at endpoint and, when at least
// one is found, registers them and makes the endpoint the local provider.
func DiscoverLocal(endpoint string, timeout time.Duration) bool {
	localEndpoint, err := url.Parse(endpoint)
	if err != nil || localEndpoint.Hostname() == "" {
		logging.Debug("Failed to parse local endpoint", "error", err, "endpoint", endpoint)
		return false
	}
	load := func(path string) []localModel {
		candidate := *localEndpoint
		candidate.Path = path
		return listLocalModelsWithTimeout(candidate.String(), timeout)
	}
	models := load(lmStudioBetaModelsPath)
	if len(models) == 0 {
		models = load(localModelsPath)
	}
	models = chatModels(models)
	if len(models) == 0 {
		logging.Debug("No local chat models found", "endpoint", endpoint)
		return false
	}
	LocalEndpoint = endpoint
	loadLocalModels(models)
	ProviderPopularity[ProviderLocal] = 0
	return true
}

// DetectLocal probes candidate endpoints in order with a short timeout and
// uses the first one that serves a chat model. It returns the endpoint used,
// or "" when none is reachable.
func DetectLocal(candidates []string, timeout time.Duration) string {
	if LocalEndpoint != "" {
		return LocalEndpoint
	}
	for _, candidate := range candidates {
		if DiscoverLocal(candidate, timeout) {
			LocalEndpointSource = "auto-detected"
			return candidate
		}
	}
	return ""
}

// chatModels drops embedding-only models, which cannot serve chat requests
// but are often listed first by Ollama (for example nomic-embed-text).
func chatModels(models []localModel) []localModel {
	var chat []localModel
	for _, model := range models {
		if model.Type == "embeddings" || strings.Contains(strings.ToLower(model.ID), "embed") {
			continue
		}
		chat = append(chat, model)
	}
	return chat
}

type localModelList struct {
	Data []localModel `json:"data"`
}

type localModel struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	Type                string `json:"type"`
	Publisher           string `json:"publisher"`
	Arch                string `json:"arch"`
	CompatibilityType   string `json:"compatibility_type"`
	Quantization        string `json:"quantization"`
	State               string `json:"state"`
	MaxContextLength    int64  `json:"max_context_length"`
	LoadedContextLength int64  `json:"loaded_context_length"`
}

func listLocalModels(modelsEndpoint string) []localModel {
	return listLocalModelsWithTimeout(modelsEndpoint, 10*time.Second)
}

func listLocalModelsWithTimeout(modelsEndpoint string, timeout time.Duration) []localModel {
	parsed, err := url.Parse(modelsEndpoint)
	if err != nil || parsed.Hostname() == "" {
		logging.Debug("Failed to parse local model endpoint", "endpoint", modelsEndpoint, "error", err)
		return []localModel{}
	}
	response, err := (network.Policy{AllowedHosts: []string{parsed.Hostname()}, AllowPrivate: true, Timeout: timeout, MaxBody: 8 << 20}).Fetch(context.Background(), modelsEndpoint)
	if err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
		return []localModel{}
	}
	if response.StatusCode != http.StatusOK {
		logging.Debug("Failed to list local models",
			"status", response.StatusCode,
			"endpoint", modelsEndpoint,
		)
		return []localModel{}
	}

	var modelList localModelList
	if err = json.Unmarshal(response.Body, &modelList); err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
		return []localModel{}
	}

	var supportedModels []localModel
	for _, model := range modelList.Data {
		if strings.HasSuffix(modelsEndpoint, lmStudioBetaModelsPath) {
			if model.Object != "model" || model.Type != "llm" {
				logging.Debug("Skipping unsupported LMStudio model",
					"endpoint", modelsEndpoint,
					"id", model.ID,
					"object", model.Object,
					"type", model.Type,
				)

				continue
			}
		}

		supportedModels = append(supportedModels, model)
	}

	return supportedModels
}

// DefaultLocalModelID is the model discovered from LOCAL_ENDPOINT that agents
// use when no model is configured. It is kept outside viper because
// config.Load resets viper and must re-apply local defaults afterwards.
var DefaultLocalModelID ModelID

func loadLocalModels(models []localModel) {
	for i, m := range models {
		model := convertLocalModel(m)
		SupportedModels[model.ID] = model

		if i == 0 || m.State == "loaded" {
			DefaultLocalModelID = model.ID
		}
	}
	ApplyLocalDefaults()
}

// ApplyLocalDefaults registers the discovered local model and a placeholder
// API key as viper defaults. Explicit configuration still takes precedence.
func ApplyLocalDefaults() {
	if DefaultLocalModelID == "" {
		return
	}
	viper.SetDefault("agents.coder.model", DefaultLocalModelID)
	viper.SetDefault("agents.summarizer.model", DefaultLocalModelID)
	viper.SetDefault("agents.task.model", DefaultLocalModelID)
	viper.SetDefault("agents.title.model", DefaultLocalModelID)
	viper.SetDefault("providers.local.apiKey", "dummy")
}

func convertLocalModel(model localModel) Model {
	contextWindow := model.LoadedContextLength
	if contextWindow <= 0 {
		contextWindow = model.MaxContextLength
	}
	if contextWindow <= 0 {
		contextWindow = 4096
	}
	return Model{
		ID:               ModelID("local." + model.ID),
		Name:             friendlyModelName(model.ID),
		Provider:         ProviderLocal,
		APIModel:         model.ID,
		ContextWindow:    contextWindow,
		DefaultMaxTokens: contextWindow,
		// OpenAI-compatible local servers (Ollama, LM Studio) reject a
		// reasoning_effort parameter for models without a thinking mode, and
		// the models endpoint does not report that capability. Reasoning is
		// therefore opt-in with LOCAL_ENDPOINT_REASONING=true.
		CanReason:           localReasoningEnabled(),
		SupportsAttachments: true,
	}
}

func localReasoningEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("LOCAL_ENDPOINT_REASONING")))
	return err == nil && enabled
}

var modelInfoRegex = regexp.MustCompile(`(?i)^([a-z0-9]+)(?:[-_]?([rv]?\d[\.\d]*))?(?:[-_]?([a-z]+))?.*`)

func friendlyModelName(modelID string) string {
	mainID := modelID
	tag := ""

	if slash := strings.LastIndex(mainID, "/"); slash != -1 {
		mainID = mainID[slash+1:]
	}

	if at := strings.Index(modelID, "@"); at != -1 {
		mainID = modelID[:at]
		tag = modelID[at+1:]
	}

	match := modelInfoRegex.FindStringSubmatch(mainID)
	if match == nil {
		return modelID
	}

	capitalize := func(s string) string {
		if s == "" {
			return ""
		}
		runes := []rune(s)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}

	family := capitalize(match[1])
	version := ""
	label := ""

	if len(match) > 2 && match[2] != "" {
		version = strings.ToUpper(match[2])
	}

	if len(match) > 3 && match[3] != "" {
		label = capitalize(match[3])
	}

	var parts []string
	if family != "" {
		parts = append(parts, family)
	}
	if version != "" {
		parts = append(parts, version)
	}
	if label != "" {
		parts = append(parts, label)
	}
	if tag != "" {
		parts = append(parts, tag)
	}

	return strings.Join(parts, " ")
}
