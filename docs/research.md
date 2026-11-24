# Research: RSCH compared with three open-source coding-agent CLIs

Compiled on 2026-11-24 for planning. It compares RSCH with three projects:

| Project | Language | License | Sources read |
| --- | --- | --- | --- |
| [xai-org/grok-build](https://github.com/xai-org/grok-build) | Rust | Apache-2.0 (third-party ports, incl. codex and sst/opencode tool code, listed in its `THIRD-PARTY-NOTICES`) | README; user guide in `crates/codegen/xai-grok-pager/docs/user-guide/` (07 MCP, 08 skills, 09 plugins, 11 custom models, 14 headless, 16 subagents, 17 sessions, 18 sandbox, 22 permissions, 24 usage); crate list |
| [anomalyco/opencode](https://github.com/anomalyco/opencode) (formerly sst/opencode, TypeScript) | TypeScript | MIT | README; [docs/agents](https://opencode.ai/docs/agents); [docs/mcp-servers](https://opencode.ai/docs/mcp-servers); `packages/` list |
| [openai/codex](https://github.com/openai/codex) | Rust | Apache-2.0 (has a `NOTICE`) | README; `docs/`; `codex-rs/cli/src/main.rs` and `mcp_cmd.rs`; `codex-rs/` crate list; [docs/subagents](https://developers.openai.com/codex/subagents) |

RSCH itself builds on [opencode-ai/opencode](https://github.com/opencode-ai/opencode), the archived **Go**
OpenCode (MIT). It is not related to the TypeScript project above beyond the shared name and history. No
source code from any of the three projects was copied into RSCH for this document. If code is ever ported
from the Apache-2.0 projects, their license text and change notices must be added to `NOTICE`.

## Feature matrix

Legend: ✅ RSCH has it and it was checked by running it or reading code and tests on 2026-11-24 · 🟡 partial,
or present but not verified end to end · ❌ not present. For the other three, each cell summarises their
README, docs or code as read; none of them were run.

| Area | RSCH (this repo) | Grok Build | OpenCode (TS) | Codex CLI |
| --- | --- | --- | --- | --- |
| **TUI** | ✅ Bubble Tea TUI (upstream), with session, model, permission and log dialogs | Full-screen, mouse-interactive TUI, themes, status line | TUI plus a desktop app (beta) | `codex-rs/tui`; desktop app via `codex app` |
| **Agents / subagents** | 🟡 Upstream `agent` tool launches a stateless, read-only search sub-agent (glob, grep, ls, view). `rsch agent` manages declarative repo agents and `route`. No parallel writable subagents and no subagent UI | Subagents with their own context and toolset, built-in agent types, personas | Primary agents (`build`, `plan`), subagents (`general`, `explore`) in child sessions, custom agents in Markdown or JSONC with per-agent permissions and `steps` | Subagent workflows enabled by default, `/subagents` to switch threads, custom agents in `.codex/agents/*.toml`, `max_concurrent_threads_per_session` |
| **Harness / tools** | ✅ view, edit, patch, write, grep, glob, ls, bash, fetch, sourcegraph, diagnostics, browser (WebDriver capability-gated); a repeat-tool guard (`ErrNoProgress` after 3 identical calls) | Terminal, file edit, search, web search, background tasks | Read, edit, shell, web, skill tools with permission actions | apply-patch, shell, file search, exec policy (`execpolicy`) |
| **Approvals / sandbox** | ✅ Permission service, with non-interactive mode denying by default (`--auto-approve` to opt in). ✅ bubblewrap detected on Linux. 🟡 macOS Seatbelt (`sandbox-exec`) implemented but **not run here** | Permission modes (always-approve, auto, ask, auto-review), rules, sandbox profiles (workspace, read-only, strict) | Ordered allow/ask/deny rules per action and resource | Sandbox command, Linux sandbox (bwrap), Windows sandbox crates, approval policies, exec policy rules |
| **MCP** | ✅ Client (`rsch mcp add/list/inspect/remove`). ✅ **Server** (`rsch mcp serve`, stdio, 3 read-only tools, tested with MCP Inspector) | Client: stdio, HTTP/SSE, streamable HTTP, CLI management | Client: local stdio and remote Streamable HTTP with OAuth, `/mcps`, Code Mode | Client: `codex mcp list/get/add/remove/login/logout`, stdio and streamable HTTP. No `mcp-server` subcommand found in `cli/src/main.rs` at HEAD |
| **Providers / local models** | ✅ OpenAI, Anthropic, Gemini, Bedrock, Groq, Azure, Vertex, Copilot, OpenRouter, xAI, mock, routed fallback. ✅ Local by default: auto-detects Ollama, then LM Studio, or uses `LOCAL_ENDPOINT` (2026-11-24; small models loop) | Chat Completions, Responses and Messages backends; documents Ollama | Many providers via `provider/model` IDs | OpenAI and ChatGPT sign-in; `ollama` and `lmstudio` crates and an `--oss` path |
| **Sessions / checkpoints** | ✅ SQLite sessions, `rsch sessions`, `rsch resume`. ✅ `rsch checkpoint create/restore` (restore needs `--approve`; verified on a scratch repo). 🟡 `rsch rewind` | Sessions store rewind points, token usage, subagent sessions | Sessions with child sessions for subagents | `resume` and `fork` of sessions, `apply` of the last diff |
| **Headless / exec** | ✅ `rsch exec` and `-p` with text, json or versioned jsonl, `--output-schema`, `rsch server` HTTP/SSE | Headless mode with tool filtering and `--allow`/`--deny` rules; ACP | Server and SDK packages | `codex exec`, `codex review`, app-server (experimental) |
| **Context / token optimisation** | ✅ `rsch context --budget` (token-budgeted, provenance), `rsch search`. 🟡 Upstream auto-compact. ✅ `rsch cost` shows session token and cost totals | Compaction, token-estimation and codebase-graph crates | Docs warn that MCP tools consume context; Code Mode for MCP | Auto-compaction, `/status` and status line show context use |
| **Plugins / skills** | 🟡 `rsch skill` (create, install, audit, compose…), `rsch plugin` (local manifests). Not verified beyond `--help` and `skill list` | Skills (SKILL.md), plugin marketplaces, hooks | Skills, plugins (`packages/plugin`) | Skills, plugins, marketplaces, lifecycle hooks |
| **IDE integration** | 🟡 LSP client for diagnostics (upstream). ✅ MCP server can be used by any MCP-capable editor. ❌ No IDE extension or ACP | ACP for editor embedding | Desktop app, SDK | IDE extension (VS Code, Cursor, Windsurf) |

## Developer pain points

These are from threads that were opened and read on 2026-11-24. Quotes are short and attributed to the thread, not to named people.

1. **Context fills fast and usage limits run out mid-task.**
   - r/codex, ["Context window seems to fill too quickly?"](https://www.reddit.com/r/codex/comments/1vqgmeb/context_window_seems_to_fill_too_quickly/): a user saw 85% context use after a couple of exchanges. A reply blames installed MCP servers, skills and large file reads.
   - r/codex, ["Usage is nuked"](https://www.reddit.com/r/codex/comments/1w0m3nd/usage_is_nuked/): one prompt filling ~200k tokens used most of a 5-hour limit, and work stopped mid-task.
   - r/ChatGPTCoding, ["Coming from Claude Code — how do you actually work with Codex's 258k context window?"](https://www.reddit.com/r/ChatGPTCoding/comments/1wnln7l/coming_from_claude_code_how_do_you_actually_work/): replies recommend disposable sessions, plan files, and subagents for work that doesn't need shared context.
   - *RSCH relevance:* budgeted, provenance-tagged context (`rsch context`, `get_context` over MCP) and a visible token meter target exactly this.
2. **MCP tool definitions cost context even when unused.**
   - HN, ["Making MCP cheaper via CLI"](https://news.ycombinator.com/item?id=47157398): commenters say MCP tools are sent on every request and point to skill files plus CLI tools as a lower-context alternative.
   - HN, ["Yes. CLI. Always CLI. Never MCP."](https://news.ycombinator.com/item?id=47392011): one comment cites ~55K tokens of tool definitions for a multi-server setup.
   - *RSCH relevance:* `rsch mcp serve` exposes only 3 small tools. Agent-skill packaging of the same CLI commands is a roadmap item.
3. **Approval fatigue pushes people into "YOLO" mode, and sandboxes get bypassed.**
   - HN, ["Ask HN: How are you sandboxing coding agents?"](https://news.ycombinator.com/item?id=46400129): people run agents with permissions skipped under Time Machine, use bubblewrap or firejail, VMs or separate users. One team reports agents masking failures with `|| true` and swapping in a cloned workspace to dodge path deny rules. Another commenter says the permission models are "either you can't get anything done, or you throw caution to the wind".
   - HN, ["Show HN: yolo-cage"](https://news.ycombinator.com/item?id=46706796): the author describes "whack-a-mole with the permission prompts" when running agents in parallel.
   - *RSCH relevance:* default-deny non-interactive mode, OS sandbox adapters, approval-gated rewind and audit records. Tested macOS sandbox coverage is still missing.
4. **Local models loop on tool calls.**
   - r/LocalLLaMA, ["Qwen3 Coder Next Looping and OpenCode"](https://www.reddit.com/r/LocalLLaMA/comments/1r6h7g4/qwen3_coder_next_looping_and_opencode/): repeated `Read(limit=…)` calls, worse with smaller quantisations.
   - r/LocalLLaMA, ["Opencode don't run tools when set up with local ollama"](https://www.reddit.com/r/LocalLLaMA/comments/1s8mmud/opencode_dont_run_tools_when_set_up_with_local/): the advice is that Ollama's context must exceed 16K, since the prompt alone is ~10K.
   - r/LocalLLaMA, ["Question about Gemma4 + opencode on consumer hardware"](https://www.reddit.com/r/LocalLLaMA/comments/1sgjzq0/question_about_gemma4_opencode_on_consumer/): the agent repeats "Actually, I'll try ls -la /." in a loop.
   - [amirhakimnejad/opencode-loop-guard](https://github.com/amirhakimnejad/opencode-loop-guard): a third-party plugin blocking identical calls that return identical results, including A-B-A-B patterns.
   - *RSCH relevance:* RSCH reproduced this with `llama3.2:3b` on 2026-11-24. Its guard stops after 3 identical requests. A smarter guard (identical result, alternating patterns, a recovery hint to the model) is a roadmap item.

## Signing in with an existing AI subscription

Goal: let users power RSCH with subscriptions they already pay for, **only through officially sanctioned
paths**. That rules out reverse-engineered OAuth clients, borrowing another product's client ID, scraping
tokens, and reading another tool's stored credentials. Pages were opened on 2026-11-24. Terms change, so
re-check each one before shipping, and treat none of this as legal advice.

| Provider / subscription | Third-party app may use the consumer subscription? | Decisive source (opened) | Path for RSCH |
| --- | --- | --- | --- |
| **OpenAI**: ChatGPT Plus/Pro ("Sign in with ChatGPT" with plan usage) | **Allowed for open-source and locally run apps** via the official program. Paid or remotely hosted apps must request access | [Integrating Sign in with ChatGPT in your open-source app](https://developers.openai.com/cookbook/articles/sign-in-with-chatgpt.md): *"At launch, ChatGPT plan usage is available to open-source projects, personal projects that run locally, and selected private apps."* and *"If you're building a paid or remotely hosted app, join the waitlist to request access before offering it to users."* | Implement the official Sign in with ChatGPT flow (RSCH is MIT and runs locally). Otherwise use an OpenAI API key |
| **Anthropic**: Claude Pro/Max (claude.ai login) | **Not allowed** unless Anthropic has approved it beforehand | [Claude Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview): *"Unless previously approved, Anthropic does not allow third party developers to offer claude.ai login or rate limits for their products, including agents built on the Claude Agent SDK. Use the API key authentication methods described in the Quickstart instead."* | Anthropic API key (or Claude via Bedrock or Vertex with the user's cloud credentials) |
| **Kiro / AWS**: Kiro subscription | **Only Kiro's own surfaces** (IDE, CLI, Web, Crew, ACP-compatible IDEs, CI automation). Third-party harnesses routing requests outside them are not permitted | [Kiro FAQ](https://kiro.dev/faq/): *"Use through third-party automation harnesses (such as OpenClaw) that route requests outside of Kiro's native interfaces is not permitted."* | Not a model provider for RSCH. Use AWS Bedrock with the user's own AWS credentials, which is a separate, pay-per-use product |
| **GitHub Copilot**: Pro, Pro+, Business, Enterprise | **Partner-only.** GitHub announced a "formal partnership" with OpenCode. No general program for other third-party CLIs was found, so for RSCH this is **unclear / not approved** | [GitHub Changelog, 2026-01-16](https://github.blog/changelog/2026-01-16-github-copilot-now-supports-opencode/): *"GitHub Copilot now fully supports authentication with OpenCode through a formal partnership"*; [About third-party coding agents](https://docs.github.com/en/copilot/concepts/agent-capabilities/about-agents/about-third-party-coding-agents) covers GitHub-hosted agents only | Do not ship Copilot sign-in until GitHub documents a program RSCH qualifies for. **Note:** the Copilot provider inherited from upstream reads tokens from `~/.config/github-copilot/hosts.json` and `apps.json`, which are another tool's stored credentials. See roadmap item P3 |
| **Google Gemini**: Google AI Pro/Ultra, Code Assist (Google account OAuth) | **Not allowed** | [Gemini CLI terms](https://geminicli.com/docs/resources/tos-privacy/): *"Directly accessing the services powering Gemini CLI (for example, the Gemini Code Assist service) using third-party software, tools, or services (for example, using OpenClaw with Gemini CLI OAuth) is a violation of applicable terms and policies."* [Gemini CLI FAQ](https://geminicli.com/docs/resources/faq.md): *"If you would like to use a third-party coding agent with Gemini, the supported and secure method is to use a Vertex AI or Google AI Studio API key."* | Gemini API key (AI Studio) or Vertex AI. Both are already supported |
| **xAI**: SuperGrok / X Premium | **Unclear for RSCH.** xAI announced OpenCode support specifically and says more open-source integrations are "coming soon", but documents no general program, client registration or terms for other apps | [Use Grok in OpenCode (x.ai)](https://x.ai/news/grok-opencode): *"Use your SuperGrok or X Premium subscription inside OpenCode."* and *"More open-source agents and integrations are coming soon."* [API account FAQ](https://docs.x.ai/developers/faq/accounts): *"the billing is separate for Grok and xAI API."* | xAI API key (already supported, `XAI_API_KEY`). Revisit if xAI publishes a program |

**Summary:** of the six, only OpenAI offers a documented program that an open-source, locally run app like
RSCH can use without prior approval. Everywhere else, RSCH should use the provider's API key, the user's
cloud account (Bedrock, Vertex), or a local model through Ollama or LM Studio.

## Takeaways for RSCH

- Direction: local-first by default, with always-on token optimisation as the headline (roadmap items 1, 1a and 2).
- Its differentiators, measured against what these threads ask for: token-budgeted context with provenance,
  explicit default-deny approvals, auditable evidence, and serving repository tools to *other* agents over MCP.
- The largest gaps against all three: real parallel subagents with their own sessions, a stronger loop guard
  for local models, a visible context and token meter in the TUI, tested macOS sandboxing, and published releases.
