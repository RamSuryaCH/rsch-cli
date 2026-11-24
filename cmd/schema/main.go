package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// JSONSchemaType represents a JSON Schema type
type JSONSchemaType struct {
	Type                 string           `json:"type,omitempty"`
	Description          string           `json:"description,omitempty"`
	Properties           map[string]any   `json:"properties,omitempty"`
	Required             []string         `json:"required,omitempty"`
	AdditionalProperties any              `json:"additionalProperties,omitempty"`
	Enum                 []any            `json:"enum,omitempty"`
	Items                map[string]any   `json:"items,omitempty"`
	OneOf                []map[string]any `json:"oneOf,omitempty"`
	AnyOf                []map[string]any `json:"anyOf,omitempty"`
	Default              any              `json:"default,omitempty"`
}

func main() {
	schema := generateSchema()

	// Pretty print the schema
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(schema); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding schema: %v\n", err)
		os.Exit(1)
	}
}

func generateSchema() map[string]any {
	schema := map[string]any{
		"$schema":     "http://json-schema.org/draft-07/schema#",
		"title":       "RSCH Configuration",
		"description": "Configuration schema for the RSCH application",
		"type":        "object",
		"properties":  map[string]any{},
	}

	// Add Data configuration
	schema["properties"].(map[string]any)["data"] = map[string]any{
		"type":        "object",
		"description": "Storage configuration",
		"properties": map[string]any{
			"directory": map[string]any{
				"type":        "string",
				"description": "Directory where application data is stored",
				"default":     ".rsch",
			},
		},
	}

	// Add working directory
	schema["properties"].(map[string]any)["wd"] = map[string]any{
		"type":        "string",
		"description": "Working directory for the application",
	}

	// Add debug flags
	schema["properties"].(map[string]any)["debug"] = map[string]any{
		"type":        "boolean",
		"description": "Enable debug mode",
		"default":     false,
	}

	schema["properties"].(map[string]any)["debugLSP"] = map[string]any{
		"type":        "boolean",
		"description": "Enable LSP debug mode",
		"default":     false,
	}

	schema["properties"].(map[string]any)["contextPaths"] = map[string]any{
		"type":        "array",
		"description": "Context paths for the application",
		"items": map[string]any{
			"type": "string",
		},
		"default": []string{
			".github/copilot-instructions.md",
			".cursorrules",
			".cursor/rules/",
			"CLAUDE.md",
			"CLAUDE.local.md",
			"rsch.md",
			"rsch.local.md",
			"RSCH.md",
			"RSCH.local.md",
			"opencode.md",
			"opencode.local.md",
			"OpenCode.md",
			"OpenCode.local.md",
			"OPENCODE.md",
			"OPENCODE.local.md",
		},
	}

	schema["properties"].(map[string]any)["tui"] = map[string]any{
		"type":        "object",
		"description": "Terminal User Interface configuration",
		"properties": map[string]any{
			"theme": map[string]any{
				"type":        "string",
				"description": "TUI theme name",
				"default":     "rsch",
				"enum": []string{
					"rsch",
					"opencode",
					"catppuccin",
					"dracula",
					"flexoki",
					"gruvbox",
					"monokai",
					"onedark",
					"tokyonight",
					"tron",
				},
			},
		},
	}

	// Add MCP servers. A missing type is the backwards-compatible stdio form;
	// SSE entries must opt in explicitly and provide a URL.
	mcpServerProperties := map[string]any{
		"command": map[string]any{
			"type":        "string",
			"description": "Executable for a stdio MCP server",
		},
		"env": map[string]any{
			"type":        "array",
			"description": "Environment entries passed to a stdio MCP server",
			"items":       map[string]any{"type": "string"},
		},
		"args": map[string]any{
			"type":        "array",
			"description": "Arguments passed to a stdio MCP server",
			"items":       map[string]any{"type": "string"},
		},
		"type": map[string]any{
			"type":        "string",
			"description": "MCP transport; omitted means stdio",
			"enum":        []string{"stdio", "sse"},
			"default":     "stdio",
		},
		"url": map[string]any{
			"type":        "string",
			"description": "URL for an SSE MCP server",
		},
		"headers": map[string]any{
			"type":                 "object",
			"description":          "HTTP headers for an SSE MCP server",
			"additionalProperties": map[string]any{"type": "string"},
		},
	}
	schema["properties"].(map[string]any)["mcpServers"] = map[string]any{
		"type":        "object",
		"description": "Model Context Protocol server configurations",
		"additionalProperties": map[string]any{
			"type":       "object",
			"properties": mcpServerProperties,
			"oneOf": []map[string]any{
				{
					"required": []string{"command"},
					"properties": map[string]any{
						"type": map[string]any{"enum": []string{"stdio"}},
					},
				},
				{
					"required": []string{"type", "url"},
					"properties": map[string]any{
						"type": map[string]any{"enum": []string{"sse"}},
					},
				},
			},
		},
	}

	// Add providers
	providerSchema := map[string]any{
		"type":        "object",
		"description": "LLM provider configurations",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "Provider configuration",
			"properties": map[string]any{
				"apiKey": map[string]any{
					"type":        "string",
					"description": "API key for the provider",
				},
				"disabled": map[string]any{
					"type":        "boolean",
					"description": "Whether the provider is disabled",
					"default":     false,
				},
			},
		},
	}

	// Add known providers. Provider names are the map keys; Provider itself
	// only contains apiKey and disabled.
	knownProviders := []string{
		string(models.ProviderAnthropic),
		string(models.ProviderOpenAI),
		string(models.ProviderGemini),
		string(models.ProviderGROQ),
		string(models.ProviderOpenRouter),
		string(models.ProviderBedrock),
		string(models.ProviderAzure),
		string(models.ProviderVertexAI),
		string(models.ProviderCopilot),
		string(models.ProviderXAI),
		string(models.ProviderLocal),
	}

	providerSchema["additionalProperties"].(map[string]any)["description"] = "Provider configuration; supported names include " + fmt.Sprint(knownProviders)
	schema["properties"].(map[string]any)["providers"] = providerSchema

	// Add agents
	agentSchema := map[string]any{
		"type":        "object",
		"description": "Agent configurations",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "Agent configuration",
			"properties": map[string]any{
				"model": map[string]any{
					"type":        "string",
					"description": "Model ID for the agent",
				},
				"maxTokens": map[string]any{
					"type":        "integer",
					"description": "Maximum tokens for the agent",
					"minimum":     1,
				},
				"timeoutMs": map[string]any{
					"type": "integer", "minimum": 0, "description": "Maximum agent execution duration in milliseconds",
				},
				"reasoningEffort": map[string]any{
					"type":        "string",
					"description": "Reasoning effort for models that support it (OpenAI, Anthropic)",
					"enum":        []string{"low", "medium", "high"},
				},
			},
			"required": []string{"model"},
		},
	}

	// Add model enum
	modelEnum := []string{}
	for modelID := range models.SupportedModels {
		modelEnum = append(modelEnum, string(modelID))
	}
	sort.Strings(modelEnum)
	agentSchema["additionalProperties"].(map[string]any)["properties"].(map[string]any)["model"].(map[string]any)["enum"] = modelEnum
	agentSchema["additionalProperties"].(map[string]any)["properties"].(map[string]any)["fallbackModels"] = map[string]any{
		"type":        "array",
		"description": "Ordered fallback model IDs used when the primary provider fails",
		"items":       map[string]any{"type": "string", "enum": modelEnum},
	}

	// Add specific agent properties
	agentProperties := map[string]any{}
	knownAgents := []string{
		string(config.AgentCoder),
		string(config.AgentSummarizer),
		string(config.AgentTask),
		string(config.AgentTitle),
	}

	for _, agentName := range knownAgents {
		agentProperties[agentName] = map[string]any{
			"$ref": "#/definitions/agent",
		}
	}

	// Create a combined schema that allows both specific agents and additional ones
	combinedAgentSchema := map[string]any{
		"type":                 "object",
		"description":          "Agent configurations",
		"properties":           agentProperties,
		"additionalProperties": agentSchema["additionalProperties"],
	}

	schema["properties"].(map[string]any)["agents"] = combinedAgentSchema
	schema["definitions"] = map[string]any{
		"agent": agentSchema["additionalProperties"],
	}
	routingRule := map[string]any{
		"type":        "object",
		"description": "Capability-aware model candidates for a task class",
		"properties": map[string]any{
			"models": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": modelEnum},
			},
			"maxCostPerMillion": map[string]any{"type": "number", "minimum": 0},
			"maxLatencyMs":      map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"models"},
	}
	routingSchema := map[string]any{
		"type":                 "object",
		"description":          "Task-class model routing rules",
		"properties":           map[string]any{},
		"additionalProperties": routingRule,
	}
	for _, class := range []string{"fast", "coding", "reasoning", "vision", "review", "security"} {
		routingSchema["properties"].(map[string]any)[class] = map[string]any{"$ref": "#/definitions/routingRule"}
	}
	schema["properties"].(map[string]any)["routing"] = routingSchema
	schema["definitions"].(map[string]any)["routingRule"] = routingRule

	// Add LSP configuration. Disabled LSP entries do not need a command; an
	// enabled entry does, matching config.Validate.
	schema["properties"].(map[string]any)["lsp"] = map[string]any{
		"type":        "object",
		"description": "Language Server Protocol configurations",
		"additionalProperties": map[string]any{
			"type":        "object",
			"description": "LSP configuration for a language",
			"properties": map[string]any{
				"disabled": map[string]any{
					"type":        "boolean",
					"description": "Whether the LSP is disabled",
					"default":     false,
				},
				"command": map[string]any{
					"type":        "string",
					"description": "Command to execute for the LSP server",
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Command arguments for the LSP server",
					"items": map[string]any{
						"type": "string",
					},
				},
				"options": map[string]any{
					"description": "Additional options for the LSP server; accepts any JSON value",
				},
			},
			"anyOf": []map[string]any{
				{"required": []string{"command"}},
				{"required": []string{"disabled"}, "properties": map[string]any{
					"disabled": map[string]any{"const": true},
				}},
			},
		},
	}
	schema["properties"].(map[string]any)["shell"] = map[string]any{
		"type":        "object",
		"description": "Shell used by the bash tool",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Shell executable path",
			},
			"args": map[string]any{
				"type":        "array",
				"description": "Arguments passed to the shell",
				"items":       map[string]any{"type": "string"},
			},
		},
	}
	schema["properties"].(map[string]any)["autoCompact"] = map[string]any{
		"type":        "boolean",
		"description": "Automatically summarize conversations near the model context limit",
		"default":     true,
	}
	schema["properties"].(map[string]any)["autonomy"] = map[string]any{
		"type":        "string",
		"description": "Execution autonomy for governed tool actions",
		"enum":        []string{"manual", "assisted", "autonomous"},
		"default":     "assisted",
	}
	schema["properties"].(map[string]any)["notifications"] = map[string]any{
		"type": "object", "description": "Optional external workflow notification delivery",
		"properties": map[string]any{
			"webhookUrl": map[string]any{"type": "string", "description": "HTTP(S) endpoint; its hostname must also appear in network.allowedHosts"},
			"command":    map[string]any{"type": "string", "description": "Non-shell executable; the exact value must also appear in policy.commandAllow and runs in an OS sandbox"},
			"args":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"timeoutMs":  map[string]any{"type": "integer", "minimum": 0},
		},
	}
	schema["properties"].(map[string]any)["policy"] = map[string]any{
		"type": "object", "description": "Central policy and required security controls",
		"properties": map[string]any{
			"tool":                map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"pathAllow":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"commandAllow":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"productionDenied":    map[string]any{"type": "boolean"},
			"requireMCPIsolation": map[string]any{"type": "boolean", "description": "Fail closed until MCP transport has an OS sandbox adapter"},
			"requireLSPIsolation": map[string]any{"type": "boolean", "description": "Fail closed until language servers can run inside an OS sandbox"},
		},
	}
	schema["properties"].(map[string]any)["governance"] = map[string]any{
		"type":        "object",
		"description": "Organization-level model, plugin, MCP, and verification controls",
		"properties": map[string]any{
			"approvedModels":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"approvedPlugins":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"approvedMCPServers":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"sharedSkills":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"sharedAgents":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"requireVerification": map[string]any{"type": "boolean"},
			"maxCost":             map[string]any{"type": "number", "minimum": 0},
		},
	}
	schema["properties"].(map[string]any)["budget"] = map[string]any{
		"type": "object", "description": "Optional task and session cost ceilings",
		"properties": map[string]any{
			"task":    map[string]any{"type": "number", "minimum": 0},
			"session": map[string]any{"type": "number", "minimum": 0},
		},
	}
	schema["properties"].(map[string]any)["resources"] = map[string]any{
		"type": "object", "description": "Validated concurrency and resource limits",
		"properties": map[string]any{
			"maxAgents":          map[string]any{"type": "integer", "minimum": 1, "default": 8},
			"maxConcurrentTools": map[string]any{"type": "integer", "minimum": 1, "default": 8},
			"maxModelRequests":   map[string]any{"type": "integer", "minimum": 1, "default": 4},
			"maxWorktrees":       map[string]any{"type": "integer", "minimum": 1, "default": 4},
			"maxBackgroundJobs":  map[string]any{"type": "integer", "minimum": 1, "default": 2},
		},
	}
	schema["properties"].(map[string]any)["network"] = map[string]any{
		"type": "object", "description": "Fail-closed network and origin policy",
		"properties": map[string]any{
			"allowedHosts": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"allowPrivate": map[string]any{"type": "boolean", "default": false},
			"timeoutMs":    map[string]any{"type": "integer", "minimum": 0},
			"maxBodyBytes": map[string]any{"type": "integer", "minimum": 0},
		},
	}

	return schema
}
