# RSCH usage reference

This is the detailed configuration and usage reference that used to live in
the README. Much of it describes behaviour inherited from upstream OpenCode
(TUI, providers, custom commands, LSP); RSCH-specific commands are listed in
`rsch --help`. Confirm any flag with `rsch <command> --help` before relying on
it.

## Configuration

RSCH uses `rsch.json` for project or user configuration, stores local state in
`.rsch/`, and exposes `RSCH_*` environment variables. Existing OpenCode
instruction files remain readable during migration, but new user-facing
commands and paths use RSCH naming.

Useful inspection and workflow commands:

```bash
rsch status
rsch doctor
rsch doctor --json
rsch spec new my-feature
rsch spec validate my-feature
rsch evidence --json
rsch spec audit /path/to/RSCH_CLI_Ultimate_Master_Prompt.md --json
rsch server --port 4096
rsch research source add https://example.com/report
rsch research evidence add <source-id> "quoted evidence" --location "p. 4"
rsch research claim add "A traceable claim" --evidence <evidence-id>
rsch verify --json
rsch verify --gate git --json
rsch verify --gate webdriver --json
```

Analysis and verification commands persist redacted evidence under
`.rsch/evidence`; the same records are available from `rsch evidence` and
`/v1/workspace/evidence`. The specification audit reports registry coverage
and concrete verification references under `.rsch/requirements/audit.json`.

Research records are persisted under `.rsch/research/records.json` with source,
evidence, claim, location, confidence, and contradiction metadata. Memory is
stored under `.rsch/memory/entries.json` with explicit layer/kind,
importance/confidence, expiry, provenance, and ranked retrieval fields. The
interactive workflow dashboard opens with `Alt+W`; press `1`–`7` to switch
between agents, tasks, memory, research, sources, sessions, and workspace.

`rsch verify` reports explicit gate states (`PASS`, `FAIL`, `BLOCKED`,
`UNVERIFIED`, or `NOT_APPLICABLE`) and only reports release readiness when all
required gates pass. The Git gate uses an isolated temporary fixture. The
WebDriver gate requires `RSCH_WEBDRIVER_URL` and performs a real session,
interaction, and clean shutdown; an unavailable endpoint remains blocked.
On macOS Safari, enable Safari Settings → Developer → Allow Remote Automation,
start `safaridriver -p 4444`, and set
`RSCH_WEBDRIVER_URL=http://127.0.0.1:4444` before retrying the gate.

The server binds to loopback by default. Set `RSCH_SERVER_TOKEN` before using
`rsch server --host <non-loopback-address>`; all endpoints except `/healthz`
then require `Authorization: Bearer <token>`.

RSCH merges configuration from lowest to highest precedence in the following
locations:

- the platform user configuration directory: `rsch/config.json`
- `$HOME/.rsch/config.json`
- `./.rsch/config.json`
- `./rsch.json`
- `$RSCH_SESSION_CONFIG` (optional session override)
- `$RSCH_COMMAND_CONFIG` (optional single-command override)

Inspect the effective, secret-redacted result with `rsch config hierarchy --json`.
`rsch doctor --json` returns stable check records, failure/warning counts,
platform/runtime details, and the detected sandbox capability for automation.
Malformed layers fail configuration loading; they are not silently ignored.

Agents may declare ordered model fallbacks. Each fallback must name a supported
model with a configured provider:

```json
{
  "agents": {
    "coder": {
      "model": "gpt-4.1",
      "fallbackModels": ["openrouter.claude-3.7-sonnet"]
    }
  }
}
```

Failed streaming attempts are discarded before a fallback is emitted, and the
selected provider/model transition is recorded in the structured event log.

Task-class routing can provide explicit candidate sets for `fast`, `coding`,
`reasoning`, `vision`, `review`, and `security` work. Candidates are validated
against provider credentials and governance before capability-aware ordering:

```json
{
  "routing": {
    "security": {
      "models": ["gpt-4.1", "claude-4-sonnet"],
      "maxCostPerMillion": 25,
      "maxLatencyMs": 30000
    }
  }
}
```

### Auto Compact Feature

RSCH includes an auto compact feature that automatically summarizes your conversation when it approaches the model's context window limit. When enabled (default setting), this feature:

- Monitors token usage during your conversation
- Automatically triggers summarization when usage reaches 95% of the model's context window
- Creates a new session with the summary, allowing you to continue your work without losing context
- Helps prevent "out of context" errors that can occur with long conversations

You can enable or disable this feature in your configuration file:

```json
{
  "autoCompact": true
}
```

`autoCompact` defaults to `true` when omitted.

### Environment Variables

You can configure RSCH using environment variables:

| Environment Variable       | Purpose                                                                          |
| -------------------------- | -------------------------------------------------------------------------------- |
| `ANTHROPIC_API_KEY`        | For Claude models                                                                |
| `OPENAI_API_KEY`           | For OpenAI models                                                                |
| `GEMINI_API_KEY`           | For Google Gemini models                                                         |
| `GITHUB_TOKEN`             | For Github Copilot models (see [Using Github Copilot](#using-github-copilot))    |
| `VERTEXAI_PROJECT`         | For Google Cloud VertexAI (Gemini)                                               |
| `VERTEXAI_LOCATION`        | For Google Cloud VertexAI (Gemini)                                               |
| `GROQ_API_KEY`             | For Groq models                                                                  |
| `AWS_ACCESS_KEY_ID`        | For AWS Bedrock (Claude)                                                         |
| `AWS_SECRET_ACCESS_KEY`    | For AWS Bedrock (Claude)                                                         |
| `AWS_REGION`               | For AWS Bedrock (Claude)                                                         |
| `AZURE_OPENAI_ENDPOINT`    | For Azure OpenAI models                                                          |
| `AZURE_OPENAI_API_KEY`     | For Azure OpenAI models (optional when using Entra ID)                           |
| `AZURE_OPENAI_API_VERSION` | For Azure OpenAI models                                                          |
| `XAI_API_KEY`              | For xAI Grok models                                                             |
| `LOCAL_ENDPOINT`           | For self-hosted models                                                           |
| `SHELL`                    | Default shell to use (if not specified in config)                                |

### Provider support matrix

| Provider | Setup | Notes |
| --- | --- | --- |
| OpenAI | `OPENAI_API_KEY` | GPT and OpenAI reasoning models |
| Anthropic | `ANTHROPIC_API_KEY` | Claude models |
| Google Gemini | `GEMINI_API_KEY` | Gemini models |
| Groq | `GROQ_API_KEY` | Hosted open models |
| OpenRouter | `OPENROUTER_API_KEY` | OpenRouter model IDs |
| xAI | `XAI_API_KEY` | Grok models |
| AWS Bedrock | AWS credentials/profile and region | Claude 3.7 Sonnet; standard AWS credential discovery applies |
| Azure OpenAI | `AZURE_OPENAI_ENDPOINT` and optional `AZURE_OPENAI_API_KEY` | Entra ID can be used when the API key is omitted |
| Google Cloud Vertex AI | `VERTEXAI_PROJECT` + `VERTEXAI_LOCATION`, or Google Cloud project/region variables | Gemini models |
| GitHub Copilot | `GITHUB_TOKEN` or an existing Copilot login | Experimental; Copilot credentials take precedence when detected |
| Local | `LOCAL_ENDPOINT` | Discovers models from an OpenAI-compatible local endpoint |

When no model is specified, RSCH selects the first configured provider in its
credential-detection order: Copilot, Anthropic, OpenAI, Gemini, Groq,
OpenRouter, xAI, Bedrock, Azure, then Vertex AI. `rsch doctor --json` reports
whether a usable provider and model configuration were detected.

### Shell Configuration

RSCH allows you to configure the shell used by the bash tool. By default, it
uses the shell specified in the `SHELL` environment variable, or falls back to
`/bin/sh` on Unix-like systems (`COMSPEC`/`cmd.exe` on Windows).

You can override this in your configuration file:

```json
{
  "shell": {
    "path": "/bin/zsh",
    "args": ["-l"]
  }
}
```

This is useful if you want to use a different shell than your default system shell, or if you need to pass specific arguments to the shell.

### Configuration File Structure

```json
{
  "data": {
    "directory": ".rsch"
  },
  "autonomy": "assisted",
  "policy": {
    "productionDenied": true
  },
  "governance": {
    "approvedModels": ["claude-3.7-sonnet"],
    "approvedMCPServers": ["example"]
  },
  "providers": {
    "openai": {
      "apiKey": "your-api-key",
      "disabled": false
    },
    "anthropic": {
      "apiKey": "your-api-key",
      "disabled": false
    },
    "copilot": {
      "disabled": false
    },
    "groq": {
      "apiKey": "your-api-key",
      "disabled": false
    },
    "openrouter": {
      "apiKey": "your-api-key",
      "disabled": false
    }
  },
  "agents": {
    "coder": {
      "model": "claude-3.7-sonnet",
      "maxTokens": 5000
    },
    "task": {
      "model": "claude-3.7-sonnet",
      "maxTokens": 5000
    },
    "title": {
      "model": "claude-3.7-sonnet",
      "maxTokens": 80
    }
  },
  "shell": {
    "path": "/bin/bash",
    "args": ["-l"]
  },
  "mcpServers": {
    "example": {
      "type": "stdio",
      "command": "path/to/mcp-server",
      "env": [],
      "args": []
    }
  },
  "lsp": {
    "go": {
      "disabled": false,
      "command": "gopls"
    }
  },
  "debug": false,
  "debugLSP": false,
  "autoCompact": true
}
```

## Supported AI Models

RSCH supports a variety of AI models from different providers:

### OpenAI

- GPT-4.1 family (gpt-4.1, gpt-4.1-mini, gpt-4.1-nano)
- GPT-4.5 Preview
- GPT-4o family (gpt-4o, gpt-4o-mini)
- O1 family (o1, o1-pro, o1-mini)
- O3 family (o3, o3-mini)
- O4 Mini

### Anthropic

- Claude 4 Sonnet
- Claude 4 Opus
- Claude 3.5 Sonnet
- Claude 3.5 Haiku
- Claude 3.7 Sonnet
- Claude 3 Haiku
- Claude 3 Opus

### GitHub Copilot

- GPT-3.5 Turbo
- GPT-4
- GPT-4o
- GPT-4o Mini
- GPT-4.1
- Claude 3.5 Sonnet
- Claude 3.7 Sonnet
- Claude 3.7 Sonnet Thinking
- Claude Sonnet 4
- O1
- O3 Mini
- O4 Mini
- Gemini 2.0 Flash
- Gemini 2.5 Pro

### Google

- Gemini 2.5
- Gemini 2.5 Flash
- Gemini 2.0 Flash
- Gemini 2.0 Flash Lite

### AWS Bedrock

- Claude 3.7 Sonnet

### Groq

- Llama 4 Maverick (17b-128e-instruct)
- Llama 4 Scout (17b-16e-instruct)
- QWEN QWQ-32b
- Deepseek R1 distill Llama 70b
- Llama 3.3 70b Versatile

### Azure OpenAI

- GPT-4.1 family (gpt-4.1, gpt-4.1-mini, gpt-4.1-nano)
- GPT-4.5 Preview
- GPT-4o family (gpt-4o, gpt-4o-mini)
- O1 family (o1, o1-mini)
- O3 family (o3, o3-mini)
- O4 Mini

### Google Cloud VertexAI

- Gemini 2.5
- Gemini 2.5 Flash

## Usage

```bash
# Start RSCH
rsch

# Start with debug logging
rsch -d

# Start with a specific working directory
rsch -c /path/to/project
```

## Non-interactive Prompt Mode

You can run RSCH in non-interactive mode by passing a prompt directly as a command-line argument. This is useful for scripting, automation, or when you want a quick answer without launching the full TUI.

```bash
# Run a single prompt and print the AI's response to the terminal
rsch -p "Explain the use of context in Go"

# Dedicated headless entry point (never falls back to the TUI)
rsch exec "Explain the use of context in Go"

# Get response in JSON format
rsch exec "Explain the use of context in Go" -f json

# Stream versioned start and terminal lifecycle records
rsch exec "Run the tests" -f jsonl

# Require the final response to satisfy a workspace-local JSON Schema
rsch exec "Summarize the result" --output-schema ./result.schema.json -f json

# Run without showing the spinner (useful for scripts)
rsch -p "Explain the use of context in Go" -q
```

In this mode, RSCH processes the prompt, prints the result to standard output,
and exits. Permission requests are denied by default so the command fails
closed instead of waiting for a TUI response. An explicit `--auto-approve` or
`--yes` opt-in approves permission prompts, but does not bypass policy: denied
tools, `productionDenied`, path and command allowlists, network restrictions,
model governance, and failed validation still stop the operation.

For unattended jobs, start with explicit limits and review the resulting
`.rsch/evidence` records:

```json
{
  "autonomy": "assisted",
  "policy": {
    "productionDenied": true,
    "commandAllow": ["git", "go"],
    "pathAllow": ["/absolute/path/to/project"]
  },
  "network": {
    "allowedHosts": ["api.openai.com"]
  }
}
```

Keep `autonomy` at `assisted` unless you have reviewed the policy boundary.
Use unattended approval only with reviewed command, path, network, and
production policies. Use `autonomous` only in a deliberately governed
environment.
Use `rsch exec --output-format json` for one stable machine-readable terminal
record, or `jsonl` for ordered lifecycle records. Structured modes disable the
spinner automatically. The legacy root `--prompt` form remains supported.

By default, a spinner animation is displayed while the model is processing your query. You can disable this spinner with the `-q` or `--quiet` flag, which is particularly useful when running RSCH from scripts or automated workflows.

### Output Formats

RSCH supports the following output formats in non-interactive mode:

| Format | Description                     |
| ------ | ------------------------------- |
| `text` | Plain text output (default)     |
| `json` | One versioned terminal result object |
| `jsonl` | Versioned start and terminal lifecycle records |

The output format is implemented as a strongly-typed `OutputFormat` in the codebase, ensuring type safety and validation when processing outputs.

## Command-line Flags

| Flag              | Short | Description                                         |
| ----------------- | ----- | --------------------------------------------------- |
| `--help`          | `-h`  | Display help information                            |
| `--debug`         | `-d`  | Enable debug mode                                   |
| `--cwd`           | `-c`  | Set current working directory                       |
| `--prompt`        | `-p`  | Run a single prompt in non-interactive mode         |
| `--output-format` | `-f`  | Output format for non-interactive mode (text, json, jsonl) |
| `--output-schema` |       | Workspace-local JSON Schema required for the final response |
| `--quiet`         | `-q`  | Hide spinner in non-interactive mode                |

## Keyboard Shortcuts

### Global Shortcuts

| Shortcut | Action                                                  |
| -------- | ------------------------------------------------------- |
| `Ctrl+C` | Open quit confirmation                                  |
| `F1`     | Toggle help dialog                                      |
| `?`      | Toggle help dialog (when not in editing mode)           |
| `F6`     | View logs                                               |
| `Alt+S`  | Switch session                                          |
| `Alt+P`  | Search quick actions                                    |
| `Alt+M`  | Toggle model selection dialog                           |
| `F5`     | Add an image                                            |
| `F8`     | Switch theme                                            |
| `Alt+W`  | Open dashboard                                          |
| `Esc`    | Close current overlay/dialog or return to previous mode |

### Chat Page Shortcuts

| Shortcut | Action                                  |
| -------- | --------------------------------------- |
| `Alt+N`  | Create new session                      |
| `Esc`    | Cancel the active operation             |
| `PgUp`/`PgDn` | Scroll the transcript              |

### Editor Shortcuts

| Shortcut            | Action                                    |
| ------------------- | ----------------------------------------- |
| `Enter` or `Ctrl+S` | Send message                              |
| `Ctrl+J`            | Insert a newline                           |
| `Alt+E`             | Open the draft in an external editor       |

### Session Dialog Shortcuts

| Shortcut   | Action           |
| ---------- | ---------------- |
| `↑` or `k` | Previous session |
| `↓` or `j` | Next session     |
| `Enter`    | Select session   |
| `Esc`      | Close dialog     |

### Model Dialog Shortcuts

| Shortcut   | Action            |
| ---------- | ----------------- |
| `↑` or `k` | Move up           |
| `↓` or `j` | Move down         |
| `←` or `h` | Previous provider |
| `→` or `l` | Next provider     |
| `Esc`      | Close dialog      |

### Permission Dialog Shortcuts

| Shortcut                | Action                       |
| ----------------------- | ---------------------------- |
| `←` or `left`           | Switch options left          |
| `→` or `right` or `tab` | Switch options right         |
| `Enter`                 | Confirm the selected decision |
| `Esc`                   | Deny permission               |
| `↑`/`↓`, `PgUp`/`PgDn`, `Space`, `d` | Review/scroll the request |

Approval requires selecting an option and pressing Enter. `Space` and `d` remain
review/navigation keys, so ordinary typing never grants or denies a permission.

### Logs Page Shortcuts

| Shortcut           | Action              |
| ------------------ | ------------------- |
| `Backspace` or `q` | Return to chat page |

## AI Assistant Tools

RSCH's AI assistant has access to various tools to help with coding tasks:

### File and Code Tools

| Tool          | Description                 | Parameters                                                                               |
| ------------- | --------------------------- | ---------------------------------------------------------------------------------------- |
| `glob`        | Find files by pattern       | `pattern` (required), `path` (optional)                                                  |
| `grep`        | Search file contents        | `pattern` (required), `path` (optional), `include` (optional), `literal_text` (optional) |
| `ls`          | List directory contents     | `path` (optional), `ignore` (optional array of patterns)                                 |
| `view`        | View file contents          | `file_path` (required), `offset` (optional), `limit` (optional)                          |
| `write`       | Write to files              | `file_path` (required), `content` (required)                                             |
| `edit`        | Edit files                  | Various parameters for file editing                                                      |
| `patch`       | Apply patches to files      | `file_path` (required), `diff` (required)                                                |
| `diagnostics` | Get diagnostics information | `file_path` (optional)                                                                   |

### Other Tools

| Tool          | Description                            | Parameters                                                                                |
| ------------- | -------------------------------------- | ----------------------------------------------------------------------------------------- |
| `bash`        | Execute shell commands                 | `command` (required), `timeout` (optional)                                                |
| `fetch`       | Fetch data from URLs                   | `url` (required), `format` (required), `timeout` (optional)                               |
| `sourcegraph` | Search code across public repositories | `query` (required), `count` (optional), `context_window` (optional), `timeout` (optional) |
| `agent`       | Run sub-tasks with the AI agent        | `prompt` (required)                                                                       |

## Architecture

RSCH is built with a modular architecture:

- **cmd**: Command-line interface using Cobra
- **internal/app**: Core application services
- **internal/config**: Configuration management
- **internal/db**: Database operations and migrations
- **internal/llm**: LLM providers and tools integration
- **internal/tui**: Terminal UI components and layouts
- **internal/logging**: Logging infrastructure
- **internal/message**: Message handling
- **internal/session**: Session management
- **internal/lsp**: Language Server Protocol integration

## Custom Commands

RSCH supports custom commands that can be created by users to quickly send predefined prompts to the AI assistant.

### Creating Custom Commands

Custom commands are predefined prompts stored as Markdown files in one of three locations:

1. **User Commands** (prefixed with `user:`):

   ```
   $XDG_CONFIG_HOME/rsch/commands/
   ```

   (typically `~/.config/rsch/commands/` on Linux/macOS)

   or

   ```
   $HOME/.rsch/commands/
   ```

2. **Project Commands** (prefixed with `project:`):

   ```
   <PROJECT DIR>/.rsch/commands/
   ```

Each `.md` file in these directories becomes a custom command. The file name (without extension) becomes the command ID.

For example, creating a file at `~/.config/rsch/commands/prime-context.md` with content:

```markdown
RUN git ls-files
READ README.md
```

This creates a command called `user:prime-context`.

### Command Arguments

RSCH supports named arguments in custom commands using placeholders in the format `$NAME` (where NAME consists of uppercase letters, numbers, and underscores, and must start with a letter).

For example:

```markdown
# Fetch Context for Issue $ISSUE_NUMBER

RUN gh issue view $ISSUE_NUMBER --json title,body,comments
RUN git grep --author="$AUTHOR_NAME" -n .
RUN grep -R "$SEARCH_PATTERN" $DIRECTORY
```

When you run a command with arguments, RSCH will prompt you to enter values for each unique placeholder. Named arguments provide several benefits:

- Clear identification of what each argument represents
- Ability to use the same argument multiple times
- Better organization for commands with multiple inputs

### Organizing Commands

You can organize commands in subdirectories:

```
~/.config/rsch/commands/git/commit.md
```

This creates a command with ID `user:git:commit`.

### Using Custom Commands

1. Press `Alt+P` to open quick actions
2. Select your custom command (prefixed with either `user:` or `project:`)
3. Press Enter to execute the command

The content of the command file will be sent as a message to the AI assistant.

### Built-in Commands

RSCH includes several built-in commands:

| Command            | Description                                                                                         |
| ------------------ | --------------------------------------------------------------------------------------------------- |
| Initialize Project | Creates or updates the rsch.md memory file with project-specific information                    |
| Compact Session    | Manually triggers the summarization of the current session, creating a new session with the summary |

## MCP (Model Context Protocol)

RSCH implements the Model Context Protocol (MCP) to extend its capabilities through external tools. MCP provides a standardized way for the AI assistant to interact with external services and tools.

### MCP Features

- **External Tool Integration**: Connect to external tools and services via a standardized protocol
- **Tool Discovery**: Automatically discover available tools from MCP servers
- **Multiple Connection Types**:
  - **Stdio**: Communicate with tools via standard input/output
  - **SSE**: Communicate with tools via Server-Sent Events
- **Security**: Permission system for controlling access to MCP tools

### Configuring MCP Servers

MCP servers are defined in the configuration file under the `mcpServers` section:

```json
{
  "mcpServers": {
    "example": {
      "type": "stdio",
      "command": "path/to/mcp-server",
      "env": [],
      "args": []
    },
    "web-example": {
      "type": "sse",
      "url": "https://example.com/mcp",
      "headers": {
        "Authorization": "Bearer token"
      }
    }
  }
}
```

### MCP Tool Usage

Once configured, MCP tools are automatically available to the AI assistant alongside built-in tools. They follow the same permission model as other tools, requiring user approval before execution.

## LSP (Language Server Protocol)

RSCH integrates with Language Server Protocol to provide code intelligence features across multiple programming languages.

### LSP Features

- **Multi-language Support**: Connect to language servers for different programming languages
- **Diagnostics**: Receive error checking and linting information
- **File Watching**: Automatically notify language servers of file changes

### Configuring LSP

Language servers are configured in the configuration file under the `lsp` section:

```json
{
  "lsp": {
    "go": {
      "disabled": false,
      "command": "gopls"
    },
    "typescript": {
      "disabled": false,
      "command": "typescript-language-server",
      "args": ["--stdio"]
    }
  }
}
```

To disable a language server without installing its executable, use
`{"disabled": true}` for that language; an enabled entry must provide a
`command`.

### LSP Integration with AI

The AI assistant can access LSP features through the `diagnostics` tool, allowing it to:

- Check for errors in your code
- Suggest fixes based on diagnostics

While the LSP client implementation supports the full LSP protocol (including completions, hover, definition, etc.), currently only diagnostics are exposed to the AI assistant.

## Using GitHub Copilot (experimental, explicit opt-in only)

The Copilot provider is inherited from upstream and is **experimental and unsupported**. GitHub officially
supports Copilot sign-in only for named partners (see
[docs/research.md](research.md#signing-in-with-an-existing-ai-subscription)), and the provider exchanges your
token at an internal GitHub endpoint (`api.github.com/copilot_internal/v2/token`) that GitHub does not document
for third-party use. Prefer a local model or a provider API key.

RSCH uses Copilot only when you explicitly provide a token, through one of:

- the `RSCH_COPILOT_TOKEN` environment variable, or
- `providers.copilot.apiKey` in `rsch.json`.

RSCH does **not** read tokens stored by other tools (for example `~/.config/github-copilot/hosts.json` or
`apps.json`), and a generic `GITHUB_TOKEN` no longer enables Copilot.

## Using a self-hosted model provider

RSCH can also load and use models from a self-hosted (OpenAI-like) provider.
This is useful for developers who want to experiment with custom models.

### Configuring a self-hosted provider

You can use a self-hosted model by setting the `LOCAL_ENDPOINT` environment variable.
This will cause RSCH to load and use the models from the specified endpoint.

```bash
# LM Studio
LOCAL_ENDPOINT=http://localhost:1235/v1
# Ollama (include /v1; the OpenAI-compatible API lives there)
LOCAL_ENDPOINT=http://localhost:11434/v1
```

The first listed model (or the model LM Studio reports as loaded) becomes the default for every agent unless
`agents.<name>.model` is set. Local models do not receive a
`reasoning_effort` parameter unless `LOCAL_ENDPOINT_REASONING=true`, because
Ollama rejects it for models without a thinking mode. RSCH's agent prompt plus
tool definitions is roughly 10k tokens, so the server's context window must be
larger than that (Ollama defaults to 4096; create a model variant with
`PARAMETER num_ctx 16384` or set `OLLAMA_CONTEXT_LENGTH`). Small models such as
`llama3.2:3b` reach the tool loop but tend to repeat the same tool call; RSCH's
repeat guard stops the run after three identical requests.

### Configuring a self-hosted model

You can also configure a self-hosted model in the configuration file under the `agents` section:

```json
{
  "agents": {
    "coder": {
      "model": "local.granite-3.3-2b-instruct@q8_0",
      "reasoningEffort": "high"
    }
  }
}
```

