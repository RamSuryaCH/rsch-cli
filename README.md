> [!NOTE]  
> RSCH is based on the original SST OpenCode repository and preserves its MIT-licensed upstream architecture.  
> Development is continuing under a new name as we prepare for a public relaunch.  
> Follow [@charmcli](https://x.com/charmcli) or join our [Discord](https://charm.sh/chat) for updates.

# ⌬ RSCH CLI

<p align="center"><img src="https://github.com/user-attachments/assets/9ae61ef6-70e5-4876-bc45-5bcb4e52c714" width="800"></p>

> **Validation notice:** RSCH exposes explicit capability and evidence states. Native browser, sandbox, and non-host release behavior are advertised only after the corresponding runtime checks pass.

A powerful terminal-based AI assistant for developers, providing intelligent coding assistance directly in your terminal.

## Overview

RSCH is a Go-based CLI application that brings provider-neutral AI assistance to your terminal. It provides a TUI (Terminal User Interface) for interacting with various AI models to help with coding tasks, debugging, and more.

<p>For a quick video overview, check out
<a href="https://www.youtube.com/watch?v=P8luPmEa1QI"><img width="25" src="https://upload.wikimedia.org/wikipedia/commons/0/09/YouTube_full-color_icon_%282017%29.svg"> RSCH upstream architecture overview</a></p>

<a href="https://www.youtube.com/watch?v=P8luPmEa1QI"><img width="550" src="https://i3.ytimg.com/vi/P8luPmEa1QI/maxresdefault.jpg"></a><p>

## Features

- **Interactive TUI**: Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) for a smooth terminal experience
- **Multiple AI Providers**: Support for OpenAI, Anthropic Claude, Google Gemini, AWS Bedrock, Groq, Azure OpenAI, and OpenRouter
- **Session Management**: Save and manage multiple conversation sessions
- **Tool Integration**: AI can execute commands, search files, and modify code
- **Vim-like Editor**: Integrated editor with text input capabilities
- **Persistent Storage**: SQLite database for storing conversations and sessions
- **LSP Integration**: Language Server Protocol support for code intelligence
- **File Change Tracking**: Track and visualize file changes during sessions
- **External Editor Support**: Open your preferred editor for composing messages
- **Named Arguments for Custom Commands**: Create powerful custom commands with multiple named placeholders

## Installation

Release binaries are built for Linux, macOS, and Windows on both x86-64 and
ARM64. Run `rsch --version` after installation to verify the executable on your
`PATH`.

### Install script (Linux and macOS)

```bash
# Install the latest version
curl -fsSL https://raw.githubusercontent.com/RamSuryaCH/rsch/main/install | bash

# Install a specific version
curl -fsSL https://raw.githubusercontent.com/RamSuryaCH/rsch/main/install | VERSION=0.1.0 bash
```

The script verifies the release archive against `checksums.txt`, installs to
`$HOME/.rsch/bin` by default, and updates the first writable Bash, Zsh, Fish,
Ash, or POSIX-shell startup file it finds. Set `RSCH_INSTALL_DIR` to choose a
different destination or `RSCH_NO_MODIFY_PATH=1` to leave shell files unchanged.

### Homebrew (macOS and Linux)

```bash
brew install RamSuryaCH/tap/rsch
```

### Arch Linux (AUR)

```bash
yay -S rsch-bin
# or
paru -S rsch-bin
```

### Debian, Ubuntu, Fedora, and RHEL

Download the package for your architecture from the GitHub release. Package
names use `amd64` or `arm64`.

```bash
# Debian or Ubuntu
sudo apt install ./rsch-linux-amd64.deb

# Fedora or RHEL
sudo dnf install ./rsch-linux-amd64.rpm
```

### Windows

Windows releases are ZIP archives for x86-64 and ARM64. This PowerShell example
installs a pinned version without requiring administrator privileges:

```powershell
$version = "0.1.0"
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "x86_64" }
$archive = "rsch-windows-$arch.zip"
$base = "https://github.com/RamSuryaCH/rsch/releases/download/v$version"
Invoke-WebRequest "$base/$archive" -OutFile $archive
Invoke-WebRequest "$base/checksums.txt" -OutFile checksums.txt
$line = Get-Content checksums.txt | Where-Object { $_ -match "\s+$([regex]::Escape($archive))$" }
if (-not $line) { throw "Checksum not found for $archive" }
$expected = ($line -split '\s+')[0].ToLowerInvariant()
$actual = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw "Checksum verification failed" }
$installDir = Join-Path $HOME ".rsch\bin"
New-Item -ItemType Directory -Force $installDir | Out-Null
Expand-Archive $archive -DestinationPath $installDir -Force
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ';') -notcontains $installDir) {
  [Environment]::SetEnvironmentVariable("Path", "$installDir;$userPath", "User")
}
$env:Path = "$installDir;$env:Path"
rsch --version
```

### Build from source

Building the current checkout requires Go 1.26.0 or newer:

```bash
go build -trimpath -o rsch .
```

Direct `go install github.com/RamSuryaCH/rsch@latest` is intentionally not
advertised until the public repository and its declared Go module path match.

### Update

- Install-script users can rerun the same command; the replacement is staged
  and moved into place atomically after checksum verification.
- Homebrew: `brew upgrade rsch`
- AUR: `yay -Syu rsch-bin` or the equivalent command for your AUR helper.
- Debian/RPM and Windows users should install the newer release artifact using
  the same steps above.

### Uninstall

Remove the package using the method that installed it:

```bash
# Install script
rm "$HOME/.rsch/bin/rsch"

# Homebrew
brew uninstall rsch

# AUR
yay -Rns rsch-bin

# Debian/Ubuntu or Fedora/RHEL
sudo apt remove rsch
sudo dnf remove rsch
```

On Windows, remove `$HOME\.rsch\bin\rsch.exe` and remove that directory from
the user `Path`. Install-script users may also remove the two-line `# rsch`
PATH block from their shell startup file. Configuration and project history are
kept deliberately; remove `$XDG_CONFIG_HOME/rsch`, `$HOME/.rsch`, or a
project's `.rsch` directory only if you also want to delete that data.

### Release integrity and supported targets

Published archives include `checksums.txt`, a source SPDX SBOM, and GitHub build
provenance. With the GitHub CLI installed, verify provenance with:

```bash
gh attestation verify ./rsch-linux-x86_64.tar.gz --repo RamSuryaCH/rsch
```

| Operating system | Architectures | Archive/package formats |
| --- | --- | --- |
| Linux | x86-64, ARM64 | `.tar.gz`, `.deb`, `.rpm` |
| macOS | x86-64, ARM64 | `.tar.gz` |
| Windows | x86-64, ARM64 | `.zip` |

## Quick start: install, configure, check, prompt

Use this path for a first run:

```bash
# 1. Install (choose one installation method above), then verify it is on PATH.
rsch --version

# 2. Configure one provider. Environment variables keep credentials out of rsch.json.
export OPENAI_API_KEY="<your-openai-api-key>"

# 3. Check the executable, workspace, configuration, provider, and storage.
rsch doctor

# 4. Send the first prompt without starting the TUI.
rsch --prompt "Summarize this repository" --quiet
```

Replace the provider variable with the one you use from the [provider support
matrix](#provider-support-matrix). If more than one provider is configured,
set an explicit `agents.coder.model` so model selection is deterministic.

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

## Using Github Copilot

_Copilot support is currently experimental._

### Requirements
- [Copilot chat in the IDE](https://github.com/settings/copilot) enabled in GitHub settings
- One of:
  - VSCode Github Copilot chat extension
  - Github `gh` CLI
  - Neovim Github Copilot plugin (`copilot.vim` or `copilot.lua`)
  - Github token with copilot permissions

If using one of the above plugins or cli tools, make sure you use the authenticate
the tool with your github account. This should create a github token at one of the following locations:
- ~/.config/github-copilot/[hosts,apps].json
- $XDG_CONFIG_HOME/github-copilot/[hosts,apps].json

If using an explicit GitHub token, you may either set the `$GITHUB_TOKEN` environment variable or add it to `rsch.json` at `providers.copilot.apiKey`.

## Using a self-hosted model provider

RSCH can also load and use models from a self-hosted (OpenAI-like) provider.
This is useful for developers who want to experiment with custom models.

### Configuring a self-hosted provider

You can use a self-hosted model by setting the `LOCAL_ENDPOINT` environment variable.
This will cause RSCH to load and use the models from the specified endpoint.

```bash
LOCAL_ENDPOINT=http://localhost:1235/v1
```

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

## Development

### Prerequisites

- Go 1.26.0 or higher

### Building from Source

```bash
# Clone the repository
git clone <RSCH repository checkout>
cd rsch

# Build
go build -o rsch

# Run
./rsch
```

## Acknowledgments

RSCH gratefully acknowledges the contributions and support from the upstream project and its contributors:

- [@isaacphi](https://github.com/isaacphi) - For the [mcp-language-server](https://github.com/isaacphi/mcp-language-server) project which provided the foundation for our LSP client implementation
- [@adamdottv](https://github.com/adamdottv) - For the design direction and UI/UX architecture

Special thanks to the broader open source community whose tools and libraries have made this project possible.

## License

RSCH is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Here's how you can contribute:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

Please make sure to update tests as appropriate and follow the existing code style.
