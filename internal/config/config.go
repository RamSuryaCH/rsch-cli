// Package config manages application configuration from various sources.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/spf13/viper"
)

// MCPType defines the type of MCP (Model Control Protocol) server.
type MCPType string

// Supported MCP types
const (
	MCPStdio MCPType = "stdio"
	MCPSse   MCPType = "sse"
)

// MCPServer defines the configuration for a Model Control Protocol server.
type MCPServer struct {
	Command string            `json:"command"`
	Env     []string          `json:"env"`
	Args    []string          `json:"args"`
	Type    MCPType           `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type AgentName string

const (
	AgentCoder      AgentName = "coder"
	AgentSummarizer AgentName = "summarizer"
	AgentTask       AgentName = "task"
	AgentTitle      AgentName = "title"
)

// Agent defines configuration for different LLM models and their token limits.
type Agent struct {
	Model           models.ModelID   `json:"model"`
	FallbackModels  []models.ModelID `json:"fallbackModels,omitempty"`
	MaxTokens       int64            `json:"maxTokens"`
	TimeoutMS       int64            `json:"timeoutMs,omitempty"`
	ReasoningEffort string           `json:"reasoningEffort"` // For openai models low,medium,heigh
}

// RoutingRule declares an ordered, policy-validated candidate set for a task
// class. The router may reorder candidates by capability and cost, while the
// resulting list remains observable and falls back in the selected order.
type RoutingRule struct {
	Models            []models.ModelID `json:"models,omitempty"`
	MaxCostPerMillion float64          `json:"maxCostPerMillion,omitempty"`
	MaxLatencyMS      int64            `json:"maxLatencyMs,omitempty"`
}

// Provider defines configuration for an LLM provider.
type Provider struct {
	APIKey   string `json:"apiKey"`
	Disabled bool   `json:"disabled"`
}

// Data defines storage configuration.
type Data struct {
	Directory string `json:"directory,omitempty"`
}

// LSPConfig defines configuration for Language Server Protocol integration.
type LSPConfig struct {
	Disabled bool     `json:"disabled"`
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	Options  any      `json:"options"`
}

// TUIConfig defines the configuration for the Terminal User Interface.
type TUIConfig struct {
	Theme string `json:"theme,omitempty"`
}

// ShellConfig defines the configuration for the shell used by the bash tool.
type ShellConfig struct {
	Path string   `json:"path,omitempty"`
	Args []string `json:"args,omitempty"`
}

// PolicyConfig is intentionally declarative. Runtime enforcement lives in the
// execution package so tools, agents, plugins, and MCP share one gate.
type PolicyConfig struct {
	Tool                map[string]string `json:"tool,omitempty"`
	PathAllow           []string          `json:"pathAllow,omitempty"`
	CommandAllow        []string          `json:"commandAllow,omitempty"`
	ProductionDenied    bool              `json:"productionDenied,omitempty"`
	RequireMCPIsolation bool              `json:"requireMCPIsolation,omitempty"`
	RequireLSPIsolation bool              `json:"requireLSPIsolation,omitempty"`
}

// GovernanceConfig contains organization-level allowlists and shared policy
// requirements. Empty allowlists mean no additional organization restriction.
type GovernanceConfig struct {
	ApprovedModels      []string `json:"approvedModels,omitempty"`
	ApprovedPlugins     []string `json:"approvedPlugins,omitempty"`
	ApprovedMCPServers  []string `json:"approvedMCPServers,omitempty"`
	SharedSkills        []string `json:"sharedSkills,omitempty"`
	SharedAgents        []string `json:"sharedAgents,omitempty"`
	RequireVerification bool     `json:"requireVerification,omitempty"`
	MaxCost             float64  `json:"maxCost,omitempty"`
}

// NotificationConfig enables optional external delivery in addition to the
// durable local event log. Secrets belong in the environment or provider
// configuration, never in event metadata.
type NotificationConfig struct {
	WebhookURL string   `json:"webhookUrl,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	TimeoutMS  int64    `json:"timeoutMs,omitempty"`
}

// BudgetConfig defines optional monetary ceilings for a task and its parent
// session. Zero means that the dimension is not configured and must remain
// explicitly unavailable rather than being treated as unlimited evidence.
type BudgetConfig struct {
	Task    float64 `json:"task,omitempty"`
	Session float64 `json:"session,omitempty"`
}

// ResourceLimits bounds concurrent work. Zero values are replaced with safe
// defaults during configuration loading, so callers never accidentally run
// unbounded work because a limit was omitted.
type ResourceLimits struct {
	MaxAgents          int `json:"maxAgents,omitempty"`
	MaxConcurrentTools int `json:"maxConcurrentTools,omitempty"`
	MaxModelRequests   int `json:"maxModelRequests,omitempty"`
	MaxWorktrees       int `json:"maxWorktrees,omitempty"`
	MaxBackgroundJobs  int `json:"maxBackgroundJobs,omitempty"`
}

// NetworkConfig is a fail-closed network policy for agent-issued HTTP/SSE
// requests. An empty allowlist denies all network destinations.
type NetworkConfig struct {
	AllowedHosts []string `json:"allowedHosts,omitempty"`
	AllowPrivate bool     `json:"allowPrivate,omitempty"`
	TimeoutMS    int64    `json:"timeoutMs,omitempty"`
	MaxBodyBytes int64    `json:"maxBodyBytes,omitempty"`
}

// Config is the main configuration structure for the application.
type Config struct {
	Data          Data                              `json:"data"`
	WorkingDir    string                            `json:"wd,omitempty"`
	MCPServers    map[string]MCPServer              `json:"mcpServers,omitempty"`
	Providers     map[models.ModelProvider]Provider `json:"providers,omitempty"`
	LSP           map[string]LSPConfig              `json:"lsp,omitempty"`
	Agents        map[AgentName]Agent               `json:"agents,omitempty"`
	Routing       map[string]RoutingRule            `json:"routing,omitempty"`
	Debug         bool                              `json:"debug,omitempty"`
	DebugLSP      bool                              `json:"debugLSP,omitempty"`
	ContextPaths  []string                          `json:"contextPaths,omitempty"`
	TUI           TUIConfig                         `json:"tui"`
	Shell         ShellConfig                       `json:"shell,omitempty"`
	AutoCompact   bool                              `json:"autoCompact,omitempty"`
	Autonomy      string                            `json:"autonomy,omitempty"`
	Policy        PolicyConfig                      `json:"policy,omitempty"`
	Governance    GovernanceConfig                  `json:"governance,omitempty"`
	Notifications NotificationConfig                `json:"notifications,omitempty"`
	Budget        BudgetConfig                      `json:"budget,omitempty"`
	Resources     ResourceLimits                    `json:"resources,omitempty"`
	Network       NetworkConfig                     `json:"network,omitempty"`

	// WorkspaceTrusted is derived before repository configuration is merged.
	// It is runtime state, not a setting a repository may deserialize.
	WorkspaceTrusted bool `json:"-" mapstructure:"-"`
}

// Application constants
const (
	defaultDataDirectory = ".rsch"
	defaultLogLevel      = "info"
	appName              = "rsch"

	MaxTokensFallbackDefault = 4096
)

var defaultContextPaths = []string{
	".github/copilot-instructions.md",
	".cursorrules",
	".cursor/rules/",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"rsch.md",
	"rsch.local.md",
	"RSCH.md",
	"RSCH.local.md",
	// Legacy project instructions remain readable for migration compatibility.
	"opencode.md",
	"opencode.local.md",
	"OpenCode.md",
	"OpenCode.local.md",
	"OPENCODE.md",
	"OPENCODE.local.md",
}

// Global configuration instance
var cfg *Config

// Load initializes the configuration from environment variables and config files.
// If debug is true, debug mode is enabled and log level is set to debug.
// It returns an error if configuration loading fails.
func Load(workingDir string, debug bool) (*Config, error) {
	if cfg != nil {
		return cfg, nil
	}

	loaded := &Config{
		WorkingDir: workingDir,
		MCPServers: make(map[string]MCPServer),
		Providers:  make(map[models.ModelProvider]Provider),
		LSP:        make(map[string]LSPConfig),
	}

	// Configuration state from a failed attempt must not survive a retry.
	viper.Reset()
	configureViper()
	setDefaults(debug)

	// Read global config
	if err := readConfig(viper.ReadInConfig()); err != nil {
		return loaded, err
	}

	// Load and merge local config. Workspace trust is resolved from trusted
	// user-owned layers before any repository-owned settings are considered.
	workspaceTrusted, err := mergeLocalConfig(workingDir)
	if err != nil {
		return loaded, err
	}

	setProviderDefaults()

	// Apply configuration to the struct
	if err := viper.Unmarshal(loaded); err != nil {
		return loaded, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	loaded.WorkspaceTrusted = workspaceTrusted

	applyDefaultValues(loaded)
	if err := validateConfig(loaded); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	defaultLevel := slog.LevelInfo
	if loaded.Debug {
		defaultLevel = slog.LevelDebug
	}
	if os.Getenv("RSCH_DEV_DEBUG") == "true" || os.Getenv("OPENCODE_DEV_DEBUG") == "true" {
		loggingFile := fmt.Sprintf("%s/%s", loaded.Data.Directory, "debug.log")
		messagesPath := fmt.Sprintf("%s/%s", loaded.Data.Directory, "messages")

		if info, err := os.Lstat(loaded.Data.Directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return loaded, errors.New("debug data directory cannot be a symbolic link")
		} else if err != nil && !os.IsNotExist(err) {
			return loaded, fmt.Errorf("failed to inspect debug data directory: %w", err)
		}
		if err := os.MkdirAll(loaded.Data.Directory, 0o700); err != nil {
			return loaded, fmt.Errorf("failed to create directory: %w", err)
		}
		if err := os.Chmod(loaded.Data.Directory, 0o700); err != nil {
			return loaded, fmt.Errorf("failed to secure directory: %w", err)
		}
		if info, err := os.Lstat(messagesPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return loaded, errors.New("debug message directory cannot be a symbolic link")
		} else if err != nil && !os.IsNotExist(err) {
			return loaded, fmt.Errorf("failed to inspect debug message directory: %w", err)
		}
		if err := os.MkdirAll(messagesPath, 0o700); err != nil {
			return loaded, fmt.Errorf("failed to create directory: %w", err)
		}
		if err := os.Chmod(messagesPath, 0o700); err != nil {
			return loaded, fmt.Errorf("failed to secure directory: %w", err)
		}
		logging.MessageDir = messagesPath

		if info, err := os.Lstat(loggingFile); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return loaded, errors.New("debug log file cannot be a symbolic link")
		} else if err != nil && !os.IsNotExist(err) {
			return loaded, fmt.Errorf("failed to inspect debug log file: %w", err)
		}
		sloggingFileWriter, err := os.OpenFile(loggingFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return loaded, fmt.Errorf("failed to open log file: %w", err)
		}
		if err := sloggingFileWriter.Chmod(0o600); err != nil {
			sloggingFileWriter.Close()
			return loaded, fmt.Errorf("failed to secure log file: %w", err)
		}
		// Configure logger
		logger := slog.New(slog.NewTextHandler(sloggingFileWriter, &slog.HandlerOptions{
			Level:       defaultLevel,
			ReplaceAttr: logging.RedactAttr,
		}))
		slog.SetDefault(logger)
	} else {
		// Configure logger
		logger := slog.New(slog.NewTextHandler(logging.NewWriter(), &slog.HandlerOptions{
			Level:       defaultLevel,
			ReplaceAttr: logging.RedactAttr,
		}))
		slog.SetDefault(logger)
	}

	if loaded.Agents == nil {
		loaded.Agents = make(map[AgentName]Agent)
	}

	// Limit an existing title agent without losing its validated settings or
	// introducing an invalid model when no provider has been configured.
	if title, exists := loaded.Agents[AgentTitle]; exists {
		title.MaxTokens = 80
		loaded.Agents[AgentTitle] = title
	}
	cfg = loaded
	return loaded, nil
}

// configureViper sets up viper's configuration paths and environment variables.
func configureViper() {
	viper.SetConfigName(appName)
	viper.SetConfigType("json")
	viper.AddConfigPath("$HOME")
	viper.AddConfigPath(fmt.Sprintf("$XDG_CONFIG_HOME/%s", appName))
	viper.AddConfigPath(fmt.Sprintf("$HOME/.config/%s", appName))
	viper.SetEnvPrefix(strings.ToUpper(appName))
	viper.AutomaticEnv()
}

// setDefaults configures default values for configuration options.
func setDefaults(debug bool) {
	viper.SetDefault("data.directory", defaultDataDirectory)
	viper.SetDefault("contextPaths", defaultContextPaths)
	viper.SetDefault("tui.theme", "rsch")
	viper.SetDefault("autoCompact", true)
	viper.SetDefault("autonomy", "assisted")
	viper.SetDefault("resources.maxAgents", 8)
	viper.SetDefault("resources.maxConcurrentTools", 8)
	viper.SetDefault("resources.maxModelRequests", 4)
	viper.SetDefault("resources.maxWorktrees", 4)
	viper.SetDefault("resources.maxBackgroundJobs", 2)

	// Set a platform-appropriate default shell. Explicit configuration and the
	// user's SHELL/COMSPEC always take precedence.
	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		if runtime.GOOS == "windows" {
			shellPath = os.Getenv("COMSPEC")
			if shellPath == "" {
				shellPath = "cmd.exe"
			}
		} else {
			shellPath = "/bin/sh"
		}
	}
	viper.SetDefault("shell.path", shellPath)
	if runtime.GOOS == "windows" {
		viper.SetDefault("shell.args", []string{"/Q"})
	} else {
		viper.SetDefault("shell.args", []string{"-l"})
	}

	if debug {
		viper.SetDefault("debug", true)
		viper.Set("log.level", "debug")
	} else {
		viper.SetDefault("debug", false)
		viper.SetDefault("log.level", defaultLogLevel)
	}
}

// setProviderDefaults configures LLM provider defaults based on provider provided by
// environment variables and configuration file.
func setProviderDefaults() {
	// Set all API keys we can find in the environment
	// Note: Viper does not default if the json apiKey is ""
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.anthropic.apiKey", apiKey)
	}
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.openai.apiKey", apiKey)
	}
	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.gemini.apiKey", apiKey)
	}
	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.groq.apiKey", apiKey)
	}
	if apiKey := os.Getenv("OPENROUTER_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.openrouter.apiKey", apiKey)
	}
	if apiKey := os.Getenv("XAI_API_KEY"); apiKey != "" {
		viper.SetDefault("providers.xai.apiKey", apiKey)
	}
	if apiKey := os.Getenv("AZURE_OPENAI_ENDPOINT"); apiKey != "" {
		// api-key may be empty when using Entra ID credentials – that's okay
		viper.SetDefault("providers.azure.apiKey", os.Getenv("AZURE_OPENAI_API_KEY"))
	}
	if apiKey, err := LoadGitHubToken(); err == nil && apiKey != "" {
		viper.SetDefault("providers.copilot.apiKey", apiKey)
		if viper.GetString("providers.copilot.apiKey") == "" {
			viper.Set("providers.copilot.apiKey", apiKey)
		}
	}

	// Use this order to set the default models
	// 1. Copilot
	// 2. Anthropic
	// 3. OpenAI
	// 4. Google Gemini
	// 5. Groq
	// 6. OpenRouter
	// 7. AWS Bedrock
	// 8. Azure
	// 9. Google Cloud VertexAI

	// copilot configuration
	if key := viper.GetString("providers.copilot.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.CopilotGPT4o)
		viper.SetDefault("agents.summarizer.model", models.CopilotGPT4o)
		viper.SetDefault("agents.task.model", models.CopilotGPT4o)
		viper.SetDefault("agents.title.model", models.CopilotGPT4o)
		return
	}

	// Anthropic configuration
	if key := viper.GetString("providers.anthropic.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.Claude4Sonnet)
		viper.SetDefault("agents.summarizer.model", models.Claude4Sonnet)
		viper.SetDefault("agents.task.model", models.Claude4Sonnet)
		viper.SetDefault("agents.title.model", models.Claude4Sonnet)
		return
	}

	// OpenAI configuration
	if key := viper.GetString("providers.openai.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.GPT41)
		viper.SetDefault("agents.summarizer.model", models.GPT41)
		viper.SetDefault("agents.task.model", models.GPT41Mini)
		viper.SetDefault("agents.title.model", models.GPT41Mini)
		return
	}

	// Google Gemini configuration
	if key := viper.GetString("providers.gemini.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.Gemini25)
		viper.SetDefault("agents.summarizer.model", models.Gemini25)
		viper.SetDefault("agents.task.model", models.Gemini25Flash)
		viper.SetDefault("agents.title.model", models.Gemini25Flash)
		return
	}

	// Groq configuration
	if key := viper.GetString("providers.groq.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.QWENQwq)
		viper.SetDefault("agents.summarizer.model", models.QWENQwq)
		viper.SetDefault("agents.task.model", models.QWENQwq)
		viper.SetDefault("agents.title.model", models.QWENQwq)
		return
	}

	// OpenRouter configuration
	if key := viper.GetString("providers.openrouter.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.OpenRouterClaude37Sonnet)
		viper.SetDefault("agents.summarizer.model", models.OpenRouterClaude37Sonnet)
		viper.SetDefault("agents.task.model", models.OpenRouterClaude37Sonnet)
		viper.SetDefault("agents.title.model", models.OpenRouterClaude35Haiku)
		return
	}

	// XAI configuration
	if key := viper.GetString("providers.xai.apiKey"); strings.TrimSpace(key) != "" {
		viper.SetDefault("agents.coder.model", models.XAIGrok3Beta)
		viper.SetDefault("agents.summarizer.model", models.XAIGrok3Beta)
		viper.SetDefault("agents.task.model", models.XAIGrok3Beta)
		viper.SetDefault("agents.title.model", models.XAiGrok3MiniFastBeta)
		return
	}

	// AWS Bedrock configuration
	if hasAWSCredentials() {
		viper.SetDefault("agents.coder.model", models.BedrockClaude37Sonnet)
		viper.SetDefault("agents.summarizer.model", models.BedrockClaude37Sonnet)
		viper.SetDefault("agents.task.model", models.BedrockClaude37Sonnet)
		viper.SetDefault("agents.title.model", models.BedrockClaude37Sonnet)
		return
	}

	// Azure OpenAI configuration
	if os.Getenv("AZURE_OPENAI_ENDPOINT") != "" {
		viper.SetDefault("agents.coder.model", models.AzureGPT41)
		viper.SetDefault("agents.summarizer.model", models.AzureGPT41)
		viper.SetDefault("agents.task.model", models.AzureGPT41Mini)
		viper.SetDefault("agents.title.model", models.AzureGPT41Mini)
		return
	}

	// Google Cloud VertexAI configuration
	if hasVertexAICredentials() {
		viper.SetDefault("agents.coder.model", models.VertexAIGemini25)
		viper.SetDefault("agents.summarizer.model", models.VertexAIGemini25)
		viper.SetDefault("agents.task.model", models.VertexAIGemini25Flash)
		viper.SetDefault("agents.title.model", models.VertexAIGemini25Flash)
		return
	}
}

// hasAWSCredentials checks if AWS credentials are available in the environment.
func hasAWSCredentials() bool {
	// Check for explicit AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		return true
	}

	// Check for AWS profile
	if os.Getenv("AWS_PROFILE") != "" || os.Getenv("AWS_DEFAULT_PROFILE") != "" {
		return true
	}

	// Check for AWS region
	if os.Getenv("AWS_REGION") != "" || os.Getenv("AWS_DEFAULT_REGION") != "" {
		return true
	}

	// Check if running on EC2 with instance profile
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return true
	}

	return false
}

// hasVertexAICredentials checks if VertexAI credentials are available in the environment.
func hasVertexAICredentials() bool {
	// Check for explicit VertexAI parameters
	if os.Getenv("VERTEXAI_PROJECT") != "" && os.Getenv("VERTEXAI_LOCATION") != "" {
		return true
	}
	// Check for Google Cloud project and location
	if os.Getenv("GOOGLE_CLOUD_PROJECT") != "" && (os.Getenv("GOOGLE_CLOUD_REGION") != "" || os.Getenv("GOOGLE_CLOUD_LOCATION") != "") {
		return true
	}
	return false
}

func hasCopilotCredentials() bool {
	// Check for explicit Copilot parameters
	if token, _ := LoadGitHubToken(); token != "" {
		return true
	}
	return false
}

// readConfig handles the result of reading a configuration file.
func readConfig(err error) error {
	if err == nil {
		return nil
	}

	// It's okay if the config file doesn't exist
	if _, ok := err.(viper.ConfigFileNotFoundError); ok {
		return nil
	}

	return fmt.Errorf("failed to read config: %w", err)
}

// mergeLocalConfig loads and merges configuration from the local directory.
func mergeLocalConfig(workingDir string) (bool, error) {
	// Merge organization layers above the global Viper config and below the
	// repository/workspace layers. Missing optional layers are ignored; malformed
	// layers are fatal.
	if configDir, err := os.UserConfigDir(); err == nil {
		if err := mergeJSONLayer(filepath.Join(configDir, "rsch", "config.json")); err != nil {
			return false, err
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if err := mergeJSONLayer(filepath.Join(home, ".rsch", "config.json")); err != nil {
			return false, err
		}
	}

	workspaceTrusted := isWorkspaceTrusted(workingDir, viper.GetStringSlice("trust.workspaces"))
	if err := mergeWorkspaceJSONLayer(filepath.Join(workingDir, ".rsch", "config.json"), workspaceTrusted); err != nil {
		return false, err
	}
	if err := mergeWorkspaceJSONLayer(filepath.Join(workingDir, appName+".json"), workspaceTrusted); err != nil {
		return false, err
	}
	if override := strings.TrimSpace(os.Getenv("RSCH_SESSION_CONFIG")); override != "" {
		if err := mergeJSONLayer(override); err != nil {
			return false, err
		}
	}
	if override := strings.TrimSpace(os.Getenv("RSCH_COMMAND_CONFIG")); override != "" {
		if err := mergeJSONLayer(override); err != nil {
			return false, err
		}
	}

	return workspaceTrusted, nil
}

func mergeJSONLayer(path string) error {
	layer, exists, err := readJSONLayer(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	viper.MergeConfigMap(layer)
	return nil
}

func mergeWorkspaceJSONLayer(path string, trusted bool) error {
	layer, exists, err := readJSONLayer(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	layer, blocked := filterWorkspaceConfig(layer, trusted)
	if len(blocked) > 0 {
		slog.Warn("ignored protected settings from untrusted workspace configuration",
			"path", path,
			"settings", strings.Join(blocked, ", "),
			"trustEnvironment", trustedWorkspacesEnv,
		)
	}
	viper.MergeConfigMap(layer)
	return nil
}

func readJSONLayer(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read configuration %s: %w", path, err)
	}
	var layer map[string]any
	if err := json.Unmarshal(data, &layer); err != nil {
		return nil, false, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	return layer, true, nil
}

// applyDefaultValues sets default values for configuration fields that need processing.
func applyDefaultValues(cfg *Config) {
	// Set default MCP type if not specified
	for k, v := range cfg.MCPServers {
		if v.Type == "" {
			v.Type = MCPStdio
			cfg.MCPServers[k] = v
		}
	}
}

// It validates model IDs and providers, ensuring they are supported.
func validateAgent(cfg *Config, name AgentName, agent Agent) error {
	// Check if model exists.
	// Note: Dynamic query of remote endpoints (e.g. https://api.githubcopilot.com/models)
	// is not performed during config validation to preserve fast, offline, and deterministic guarantees.
	model, modelExists := models.SupportedModels[agent.Model]
	if !modelExists {
		return fmt.Errorf("agent %s specifies unsupported model %q", name, agent.Model)
	}
	seenFallbacks := make(map[models.ModelID]struct{}, len(agent.FallbackModels))
	for _, fallbackID := range agent.FallbackModels {
		fallback, exists := models.SupportedModels[fallbackID]
		if !exists {
			return fmt.Errorf("agent %s specifies unsupported fallback model %q", name, fallbackID)
		}
		if fallbackID == agent.Model {
			return fmt.Errorf("agent %s repeats primary model %q as a fallback", name, fallbackID)
		}
		if _, seen := seenFallbacks[fallbackID]; seen {
			return fmt.Errorf("agent %s repeats fallback model %q", name, fallbackID)
		}
		seenFallbacks[fallbackID] = struct{}{}
		fallbackProvider, configured := cfg.Providers[fallback.Provider]
		fallbackProvider, available := resolveProviderCredentials(fallback.Provider, fallbackProvider, configured)
		if !available {
			return fmt.Errorf("agent %s fallback model %q uses disabled or credential-less provider %q", name, fallbackID, fallback.Provider)
		}
		cfg.Providers[fallback.Provider] = fallbackProvider
	}

	// Check if provider for the model is configured
	provider := model.Provider
	providerCfg, providerExists := cfg.Providers[provider]
	providerCfg, available := resolveProviderCredentials(provider, providerCfg, providerExists)
	if !available {
		if !providerExists {
			return fmt.Errorf("agent %s requires unconfigured provider %q for model %q", name, provider, agent.Model)
		}
		return fmt.Errorf("agent %s uses disabled or credential-less provider %q", name, provider)
	}
	if !providerExists {
		logging.Info("added provider from environment", "provider", provider)
	}
	cfg.Providers[provider] = providerCfg

	// Validate max tokens
	if agent.MaxTokens <= 0 {
		// Zero means the setting was omitted and is a normal default.
		if agent.MaxTokens < 0 {
			logging.Warn("invalid max tokens, setting to default",
				"agent", name,
				"model", agent.Model,
				"max_tokens", agent.MaxTokens)
		}

		// Update the agent with default max tokens
		updatedAgent := cfg.Agents[name]
		if model.DefaultMaxTokens > 0 {
			updatedAgent.MaxTokens = model.DefaultMaxTokens
		} else {
			updatedAgent.MaxTokens = MaxTokensFallbackDefault
		}
		cfg.Agents[name] = updatedAgent
	} else if model.ContextWindow > 0 && agent.MaxTokens > model.ContextWindow/2 {
		// Ensure max tokens doesn't exceed half the context window (reasonable limit)
		logging.Warn("max tokens exceeds half the context window, adjusting",
			"agent", name,
			"model", agent.Model,
			"max_tokens", agent.MaxTokens,
			"context_window", model.ContextWindow)

		// Update the agent with adjusted max tokens
		updatedAgent := cfg.Agents[name]
		updatedAgent.MaxTokens = model.ContextWindow / 2
		cfg.Agents[name] = updatedAgent
	}

	// Validate reasoning effort for models that support reasoning
	if model.CanReason && provider == models.ProviderOpenAI || provider == models.ProviderLocal {
		if agent.ReasoningEffort == "" {
			// Set default reasoning effort for models that support it
			logging.Info("setting default reasoning effort for model that supports reasoning",
				"agent", name,
				"model", agent.Model)

			// Update the agent with default reasoning effort
			updatedAgent := cfg.Agents[name]
			updatedAgent.ReasoningEffort = "medium"
			cfg.Agents[name] = updatedAgent
		} else {
			// Check if reasoning effort is valid (low, medium, high)
			effort := strings.ToLower(agent.ReasoningEffort)
			if effort != "low" && effort != "medium" && effort != "high" {
				logging.Warn("invalid reasoning effort, setting to medium",
					"agent", name,
					"model", agent.Model,
					"reasoning_effort", agent.ReasoningEffort)

				// Update the agent with valid reasoning effort
				updatedAgent := cfg.Agents[name]
				updatedAgent.ReasoningEffort = "medium"
				cfg.Agents[name] = updatedAgent
			}
		}
	} else if !model.CanReason && agent.ReasoningEffort != "" {
		// Model doesn't support reasoning but reasoning effort is set
		logging.Warn("model doesn't support reasoning but reasoning effort is set, ignoring",
			"agent", name,
			"model", agent.Model,
			"reasoning_effort", agent.ReasoningEffort)

		// Update the agent to remove reasoning effort
		updatedAgent := cfg.Agents[name]
		updatedAgent.ReasoningEffort = ""
		cfg.Agents[name] = updatedAgent
	}

	return nil
}

// Validate checks if the configuration is valid and applies defaults where needed.
func Validate() error {
	return validateConfig(cfg)
}

func validateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	if cfg.Autonomy != "manual" && cfg.Autonomy != "assisted" && cfg.Autonomy != "autonomous" {
		return fmt.Errorf("invalid autonomy level %q", cfg.Autonomy)
	}
	if cfg.Notifications.TimeoutMS < 0 {
		return fmt.Errorf("notifications.timeoutMs cannot be negative")
	}
	if cfg.Governance.MaxCost < 0 {
		return fmt.Errorf("governance.maxCost cannot be negative")
	}
	if cfg.Budget.Task < 0 || cfg.Budget.Session < 0 {
		return fmt.Errorf("budget.task and budget.session cannot be negative")
	}
	if cfg.Resources.MaxAgents <= 0 || cfg.Resources.MaxConcurrentTools <= 0 || cfg.Resources.MaxModelRequests <= 0 || cfg.Resources.MaxWorktrees <= 0 || cfg.Resources.MaxBackgroundJobs <= 0 {
		return fmt.Errorf("all resource limits must be greater than zero")
	}
	if cfg.Network.TimeoutMS < 0 || cfg.Network.MaxBodyBytes < 0 {
		return fmt.Errorf("network timeout and body limits cannot be negative")
	}
	for tool, decision := range cfg.Policy.Tool {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("policy tool name cannot be empty")
		}
		switch strings.ToLower(strings.TrimSpace(decision)) {
		case "allow", "prompt", "deny":
		default:
			return fmt.Errorf("policy tool %q has invalid decision %q", tool, decision)
		}
	}

	// Validate agent models
	for name, agent := range cfg.Agents {
		if agent.TimeoutMS < 0 || agent.MaxTokens < 0 {
			return fmt.Errorf("agent %s resource limits cannot be negative", name)
		}
		if err := validateAgent(cfg, name, agent); err != nil {
			return err
		}
		if !approved(cfg.Governance.ApprovedModels, string(agent.Model)) {
			return fmt.Errorf("agent %s uses model %q disallowed by governance", name, agent.Model)
		}
		for _, fallbackID := range agent.FallbackModels {
			if !approved(cfg.Governance.ApprovedModels, string(fallbackID)) {
				return fmt.Errorf("agent %s uses fallback model %q disallowed by governance", name, fallbackID)
			}
		}
	}
	if err := validateRouting(cfg); err != nil {
		return err
	}
	if len(cfg.Governance.ApprovedMCPServers) > 0 {
		for name := range cfg.MCPServers {
			if !approved(cfg.Governance.ApprovedMCPServers, name) {
				return fmt.Errorf("MCP server %q disallowed by governance", name)
			}
		}
	}

	// Validate providers
	for provider, providerCfg := range cfg.Providers {
		resolved, available := resolveProviderCredentials(provider, providerCfg, true)
		if !providerCfg.Disabled && !available {
			return fmt.Errorf("provider %q has no usable credentials or required endpoint configuration; set credentials or explicitly disable it", provider)
		}
		cfg.Providers[provider] = resolved
	}

	// Validate LSP configurations
	for language, lspConfig := range cfg.LSP {
		if lspConfig.Command == "" && !lspConfig.Disabled {
			return fmt.Errorf("LSP configuration %q has no command; set a command or explicitly disable it", language)
		}
	}

	return nil
}

func validateRouting(cfg *Config) error {
	validClasses := map[string]bool{"fast": true, "coding": true, "reasoning": true, "vision": true, "review": true, "security": true}
	for class, rule := range cfg.Routing {
		if !validClasses[class] {
			return fmt.Errorf("routing specifies unsupported task class %q", class)
		}
		if len(rule.Models) == 0 {
			return fmt.Errorf("routing class %q must specify at least one model", class)
		}
		if rule.MaxCostPerMillion < 0 || rule.MaxLatencyMS < 0 {
			return fmt.Errorf("routing class %q has negative cost or latency constraint", class)
		}
		seen := make(map[models.ModelID]struct{}, len(rule.Models))
		for _, modelID := range rule.Models {
			if _, duplicate := seen[modelID]; duplicate {
				return fmt.Errorf("routing class %q repeats model %q", class, modelID)
			}
			seen[modelID] = struct{}{}
			model, ok := models.SupportedModels[modelID]
			if !ok {
				return fmt.Errorf("routing class %q specifies unsupported model %q", class, modelID)
			}
			if !approved(cfg.Governance.ApprovedModels, string(modelID)) {
				return fmt.Errorf("routing class %q uses model %q disallowed by governance", class, modelID)
			}
			providerCfg, configured := cfg.Providers[model.Provider]
			providerCfg, available := resolveProviderCredentials(model.Provider, providerCfg, configured)
			if !available {
				return fmt.Errorf("routing class %q model %q uses disabled or credential-less provider %q", class, modelID, model.Provider)
			}
			cfg.Providers[model.Provider] = providerCfg
		}
	}
	return nil
}

func approved(allowlist []string, value string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, allowed := range allowlist {
		if strings.TrimSpace(allowed) == value {
			return true
		}
	}
	return false
}

// ModelApproved and MCPApproved are reusable gates for runtime components
// that select models or connect to configured MCP servers.
func ModelApproved(model models.ModelID) bool {
	return cfg == nil || approved(cfg.Governance.ApprovedModels, string(model))
}
func MCPApproved(name string) bool {
	return cfg == nil || approved(cfg.Governance.ApprovedMCPServers, name)
}

// getProviderAPIKey gets the API key for a provider from environment variables
func getProviderAPIKey(provider models.ModelProvider) string {
	switch provider {
	case models.ProviderAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY")
	case models.ProviderOpenAI:
		return os.Getenv("OPENAI_API_KEY")
	case models.ProviderGemini:
		return os.Getenv("GEMINI_API_KEY")
	case models.ProviderGROQ:
		return os.Getenv("GROQ_API_KEY")
	case models.ProviderAzure:
		return os.Getenv("AZURE_OPENAI_API_KEY")
	case models.ProviderOpenRouter:
		return os.Getenv("OPENROUTER_API_KEY")
	case models.ProviderBedrock:
		if hasAWSCredentials() {
			return "aws-credentials-available"
		}
	case models.ProviderVertexAI:
		if hasVertexAICredentials() {
			return "vertex-ai-credentials-available"
		}
	}
	return ""
}

// resolveProviderCredentials applies the same credential rules to primary,
// fallback, routing, and standalone provider validation. It resolves API keys
// from the environment without storing synthetic credential sentinels for
// providers that use an SDK credential chain.
func resolveProviderCredentials(provider models.ModelProvider, providerCfg Provider, configured bool) (Provider, bool) {
	if configured && providerCfg.Disabled {
		return providerCfg, false
	}
	if strings.TrimSpace(providerCfg.APIKey) == "" {
		if apiKey := strings.TrimSpace(getProviderAPIKey(provider)); apiKey != "" {
			providerCfg.APIKey = apiKey
		}
	}
	return providerCfg, providerCredentialsAvailable(provider, providerCfg)
}

type azureCredentialMode string

const (
	azureCredentialUnavailable azureCredentialMode = ""
	azureCredentialAPIKey      azureCredentialMode = "api-key"
	azureCredentialDefault     azureCredentialMode = "default-credential"
)

func configuredAzureCredentialMode(providerCfg Provider) azureCredentialMode {
	if providerCfg.Disabled || strings.TrimSpace(os.Getenv("AZURE_OPENAI_ENDPOINT")) == "" || strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_VERSION")) == "" {
		return azureCredentialUnavailable
	}
	if strings.TrimSpace(providerCfg.APIKey) != "" || strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_KEY")) != "" {
		return azureCredentialAPIKey
	}
	// With complete Azure service configuration, omitting an API key is the
	// explicit declaration that the provider should use DefaultAzureCredential.
	return azureCredentialDefault
}

func providerCredentialsAvailable(provider models.ModelProvider, providerCfg Provider) bool {
	if providerCfg.Disabled {
		return false
	}
	if provider == models.ProviderAzure {
		return configuredAzureCredentialMode(providerCfg) != azureCredentialUnavailable
	}
	if provider == models.ProviderLocal {
		return strings.TrimSpace(providerCfg.APIKey) != "" || strings.TrimSpace(os.Getenv("LOCAL_ENDPOINT")) != ""
	}
	return strings.TrimSpace(providerCfg.APIKey) != ""
}

// setDefaultModelForAgent sets a default model for an agent based on available providers
func setDefaultModelForAgent(agent AgentName) bool {
	if hasCopilotCredentials() {
		maxTokens := int64(5000)
		if agent == AgentTitle {
			maxTokens = 80
		}

		cfg.Agents[agent] = Agent{
			Model:     models.CopilotGPT4o,
			MaxTokens: maxTokens,
		}
		return true
	}
	// Check providers in order of preference
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		maxTokens := int64(5000)
		if agent == AgentTitle {
			maxTokens = 80
		}
		cfg.Agents[agent] = Agent{
			Model:     models.Claude37Sonnet,
			MaxTokens: maxTokens,
		}
		return true
	}

	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		var model models.ModelID
		maxTokens := int64(5000)
		reasoningEffort := ""

		switch agent {
		case AgentTitle:
			model = models.GPT41Mini
			maxTokens = 80
		case AgentTask:
			model = models.GPT41Mini
		default:
			model = models.GPT41
		}

		// Check if model supports reasoning
		if modelInfo, ok := models.SupportedModels[model]; ok && modelInfo.CanReason {
			reasoningEffort = "medium"
		}

		cfg.Agents[agent] = Agent{
			Model:           model,
			MaxTokens:       maxTokens,
			ReasoningEffort: reasoningEffort,
		}
		return true
	}

	if apiKey := os.Getenv("OPENROUTER_API_KEY"); apiKey != "" {
		var model models.ModelID
		maxTokens := int64(5000)
		reasoningEffort := ""

		switch agent {
		case AgentTitle:
			model = models.OpenRouterClaude35Haiku
			maxTokens = 80
		case AgentTask:
			model = models.OpenRouterClaude37Sonnet
		default:
			model = models.OpenRouterClaude37Sonnet
		}

		// Check if model supports reasoning
		if modelInfo, ok := models.SupportedModels[model]; ok && modelInfo.CanReason {
			reasoningEffort = "medium"
		}

		cfg.Agents[agent] = Agent{
			Model:           model,
			MaxTokens:       maxTokens,
			ReasoningEffort: reasoningEffort,
		}
		return true
	}

	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		var model models.ModelID
		maxTokens := int64(5000)

		if agent == AgentTitle {
			model = models.Gemini25Flash
			maxTokens = 80
		} else {
			model = models.Gemini25
		}

		cfg.Agents[agent] = Agent{
			Model:     model,
			MaxTokens: maxTokens,
		}
		return true
	}

	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		maxTokens := int64(5000)
		if agent == AgentTitle {
			maxTokens = 80
		}

		cfg.Agents[agent] = Agent{
			Model:     models.QWENQwq,
			MaxTokens: maxTokens,
		}
		return true
	}

	if hasAWSCredentials() {
		maxTokens := int64(5000)
		if agent == AgentTitle {
			maxTokens = 80
		}

		cfg.Agents[agent] = Agent{
			Model:           models.BedrockClaude37Sonnet,
			MaxTokens:       maxTokens,
			ReasoningEffort: "medium", // Claude models support reasoning
		}
		return true
	}

	if hasVertexAICredentials() {
		var model models.ModelID
		maxTokens := int64(5000)

		if agent == AgentTitle {
			model = models.VertexAIGemini25Flash
			maxTokens = 80
		} else {
			model = models.VertexAIGemini25
		}

		cfg.Agents[agent] = Agent{
			Model:     model,
			MaxTokens: maxTokens,
		}
		return true
	}

	return false
}

// AddMCPServer persists a named MCP server through the configured hierarchy
// and updates the loaded configuration immediately for the current process.
func AddMCPServer(name string, server MCPServer) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid MCP server name")
	}
	if server.Type == "" {
		server.Type = MCPStdio
	}
	if server.Type != MCPStdio && server.Type != MCPSse {
		return fmt.Errorf("invalid MCP server type %q", server.Type)
	}
	if server.Type == MCPStdio && strings.TrimSpace(server.Command) == "" {
		return fmt.Errorf("stdio MCP server command is required")
	}
	if server.Type == MCPSse && strings.TrimSpace(server.URL) == "" {
		return fmt.Errorf("SSE MCP server URL is required")
	}
	if !MCPApproved(name) {
		return fmt.Errorf("MCP server %q is disallowed by governance", name)
	}
	if err := updateCfgFile(func(userCfg *Config) {
		if userCfg.MCPServers == nil {
			userCfg.MCPServers = map[string]MCPServer{}
		}
		userCfg.MCPServers[name] = server
	}); err != nil {
		return err
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]MCPServer{}
	}
	cfg.MCPServers[name] = server
	return nil
}

// RemoveMCPServer removes a configured MCP server and fails clearly when the
// named server does not exist.
func RemoveMCPServer(name string) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	name = strings.TrimSpace(name)
	if _, ok := cfg.MCPServers[name]; !ok {
		return fmt.Errorf("MCP server %q is not configured", name)
	}
	if err := updateCfgFile(func(userCfg *Config) {
		delete(userCfg.MCPServers, name)
	}); err != nil {
		return err
	}
	delete(cfg.MCPServers, name)
	return nil
}

// Get returns the current configuration.
// It's safe to call this function multiple times.
func Get() *Config {
	return cfg
}

// WorkingDirectory returns the current working directory from the configuration.
func WorkingDirectory() string {
	if cfg == nil {
		cwd, err := os.Getwd()
		if err == nil {
			return cwd
		}
		return "."
	}
	return cfg.WorkingDir
}

// ResolveDataDirectory returns the effective data directory for a workspace.
// Relative configured paths are workspace-relative, matching the process
// working directory used by the application. Empty values use the default.
func ResolveDataDirectory(root, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = defaultDataDirectory
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	if strings.TrimSpace(root) == "" {
		return filepath.Clean(configured)
	}
	return filepath.Clean(filepath.Join(root, configured))
}

func UpdateAgentModel(agentName AgentName, modelID models.ModelID) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	existingAgentCfg := cfg.Agents[agentName]

	model, ok := models.SupportedModels[modelID]
	if !ok {
		return fmt.Errorf("model %s not supported", modelID)
	}
	if !approved(cfg.Governance.ApprovedModels, string(modelID)) {
		return fmt.Errorf("agent %s uses model %q disallowed by governance", agentName, modelID)
	}
	for _, fallbackID := range existingAgentCfg.FallbackModels {
		if !approved(cfg.Governance.ApprovedModels, string(fallbackID)) {
			return fmt.Errorf("agent %s uses fallback model %q disallowed by governance", agentName, fallbackID)
		}
	}

	maxTokens := existingAgentCfg.MaxTokens
	if model.DefaultMaxTokens > 0 {
		maxTokens = model.DefaultMaxTokens
	}

	newAgentCfg := Agent{
		Model:           modelID,
		FallbackModels:  append([]models.ModelID(nil), existingAgentCfg.FallbackModels...),
		MaxTokens:       maxTokens,
		TimeoutMS:       existingAgentCfg.TimeoutMS,
		ReasoningEffort: existingAgentCfg.ReasoningEffort,
	}
	// Validation resolves providers and normalizes agent settings. Stage those
	// changes so a validation or persistence failure leaves active state intact.
	candidate := *cfg
	candidate.Agents = maps.Clone(cfg.Agents)
	if candidate.Agents == nil {
		candidate.Agents = make(map[AgentName]Agent)
	}
	candidate.Providers = maps.Clone(cfg.Providers)
	if candidate.Providers == nil {
		candidate.Providers = make(map[models.ModelProvider]Provider)
	}
	candidate.Agents[agentName] = newAgentCfg
	if err := validateAgent(&candidate, agentName, newAgentCfg); err != nil {
		return fmt.Errorf("failed to update agent model: %w", err)
	}

	if err := updateCfgFile(func(config *Config) {
		if config.Agents == nil {
			config.Agents = make(map[AgentName]Agent)
		}
		config.Agents[agentName] = candidate.Agents[agentName]
	}); err != nil {
		return err
	}
	cfg.Agents = candidate.Agents
	cfg.Providers = candidate.Providers
	return nil
}

// UpdateTheme updates the theme in the configuration and writes it to the config file.
func UpdateTheme(themeName string) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}

	if err := updateCfgFile(func(config *Config) {
		config.TUI.Theme = themeName
	}); err != nil {
		return err
	}
	cfg.TUI.Theme = themeName
	return nil
}

// Tries to load Github token from all possible locations
func LoadGitHubToken() (string, error) {
	// First check environment variable
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}

	// Get config directory
	var configDir string
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		configDir = xdgConfig
	} else if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			configDir = localAppData
		} else {
			configDir = filepath.Join(os.Getenv("HOME"), "AppData", "Local")
		}
	} else {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}

	// Try both hosts.json and apps.json files
	filePaths := []string{
		filepath.Join(configDir, "github-copilot", "hosts.json"),
		filepath.Join(configDir, "github-copilot", "apps.json"),
	}

	for _, filePath := range filePaths {
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var config map[string]map[string]interface{}
		if err := json.Unmarshal(data, &config); err != nil {
			continue
		}

		for key, value := range config {
			if strings.Contains(key, "github.com") {
				if oauthToken, ok := value["oauth_token"].(string); ok {
					return oauthToken, nil
				}
			}
		}
	}

	return "", fmt.Errorf("GitHub token not found in standard locations")
}
