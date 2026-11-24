# RSCH roadmap

Prioritised by impact (for developer productivity and the pain points in [research.md](research.md)) against
feasibility for a solo maintainer. Core direction: **local-first** (local models by default, hosted
providers bring-your-own-key) and **fewer tokens, more done**. Status labels: **done** means implemented, tested and verified;
**partial** means it exists but has a known gap; **planned** means not started.
The older slice-by-slice history is in [ROADMAP.md](ROADMAP.md).

| # | Item | Why | Status |
| --- | --- | --- | --- |
| 1 | **Always-on automatic token optimiser ("fewer tokens, more done")**: budgeted context and smart file and line selection fed into the agent loop automatically; prompt and context compression; deduplication of repeated file reads and tool results; caching of repeated context; summarising long histories (in `exec` as well as the TUI); trimming tool output by relevance, not just a length cap | The top developer pain point is context and usage exhaustion (research §1, §2) | **partial.** Done (3f0ee44, `internal/optimizer`, on by default, `--no-optimize` / `RSCH_OPTIMIZER=off`): head/tail trimming with markers, deduplication of identical tool outputs, a content-hash rewrite cache, deterministic compaction against the context window in `exec` and TUI, and per-request before/after accounting. Still planned: feeding `rsch context` into the loop automatically, relevance-based trimming, LLM summarisation in `exec` |
| 1a | **Token benchmark**: a fixed task set run with the optimiser on and off, counting tokens with a real tokenizer | Proves item 1 rather than claiming it | **done** (deterministic replay with tiktoken, [bench/tokens](../bench/tokens/README.md)): median 7.7% fewer prompt tokens over 8 tasks, 0% on a tiny repository. Live runs with provider-reported usage and task success rate are **planned** |
| 2 | **Local-first default provider** | Must work with no cloud service or API key | **done (steps 1, 2, 5, 6).** Auto-detects Ollama, then LM Studio; skips embedding models; local wins unless a model is configured; `provider-in-use` in `rsch doctor`; tests with a fake Ollama. Steps 3 and 4 are open. See [Default provider](#default-provider-today-and-what-local-first-would-take) |
| 3 | Local models through `LOCAL_ENDPOINT` (Ollama, LM Studio) | Local-first use and no paid keys | **partial.** Config and reasoning fixes landed 2026-11-24 (c1c047c). The model is reached, but small models loop |
| 4 | Smarter loop guard: identical result, A-B-A-B rotation, failure streaks, a corrective hint to the model before stopping | The most common local-model failure (research §4) | **planned.** Today's guard stops after 3 identical requests |
| 5 | Context and token meter in the TUI and `exec` JSON (prompt, tool definitions and history tokens per turn) | Makes item 1 visible (research §1) | **partial.** `rsch cost` shows persisted session totals; `rsch exec` prints estimated history tokens before and after optimisation to stderr. Nothing in the TUI or JSON yet |
| 6 | Subagent task tool: a parallel, scoped child session with its own permission profile and a summary returned to the parent | Keeps noisy work off the main context; all three comparison tools have it | **partial.** Only the upstream stateless read-only search agent exists |
| 7 | MCP server mode | Lets editors and other agents reuse RSCH's budgeted context | **done** for 3 read-only tools (10ef2dc). Next: optional `review` and `checkpoint list` tools, and HTTP transport |
| 8 | Agent-skill packaging (`SKILL.md` describing the `rsch search`/`context` CLI) | A lower-context alternative to MCP (research §2) | **planned** |
| 9 | Verify the macOS Seatbelt sandbox on Apple silicon and record the darwin test results | It is a security boundary and is currently untested | **planned.** Needs a Mac |
| 10 | Releases and Homebrew | Lets people install without Go | **planned.** See [Distribution](#distribution-plan-releases-and-homebrew). Nothing is published |
| 11 | Rename the Go module path to `github.com/RamSuryaCH/rsch-cli` | Makes `go install` work and removes the upstream name | **planned.** A mechanical import rewrite across ~500 files |
| 12 | TUI: approval dialog showing the sandbox profile and diff preview; subagent activity panel | Approval fatigue (research §3) | **planned** |
| 13 | Background job stop on Linux | Correctness | **done** (6e20e9b, zombie handling; the test now passes) |
| 14 | IDE integration via ACP | Editor embedding, as in Grok Build | **planned.** MCP server mode covers some of this |

## My picks, ranked (why each matters most)

1. **Token optimiser plus benchmark (items 1, 1a).** This is the headline promise and the top pain point
   (research §1). The benchmark turns it from a claim into a number reviewers can check.
2. **Local-first default and a stronger loop guard (items 2, 4).** Without these, "works with no cloud" fails
   on first run: there is no default provider, and small models loop. Both are small, testable changes.
3. **Provider layer clean-up (P1–P3 below).** Removing reads of another tool's stored Copilot credentials
   (done in aa51c6b) was a correctness and terms issue, and a generic OpenAI-compatible provider unlocks Ollama, LM Studio, vLLM
   and OpenRouter through one code path.
4. **Live context meter (item 5).** It makes the optimiser visible and is cheap once token accounting exists.
5. **Subagent task tool (item 6).** It has high value, but it is larger and depends on per-session token
   accounting and permission profiles.
6. **Official Sign in with ChatGPT (P5).** It is the only sanctioned subscription path for a local
   open-source app (research, subscription section). It is implemented as a standalone command and awaits a
   live check.
7. **Releases and Homebrew tap (item 10).** These matter for adoption, but they are gated on Surya's go-ahead.

## Pluggable provider layer

Audit of `internal/llm/provider` and `internal/config` on 2026-11-24:

| Provider / capability | Today | Planned |
| --- | --- | --- |
| Anthropic (native API) | **exists** (`anthropic.go`) | keep |
| OpenAI (native API) | **exists** (`openai.go`) | keep |
| Generic OpenAI-compatible endpoint with any base URL and named instances | **partial.** Groq, OpenRouter, xAI and local all reuse `newOpenAIClient`, but the only user-settable base URL is `LOCAL_ENDPOINT` (plus Azure's endpoint) | `providers.<name> = {type: "openai-compatible", baseURL, apiKeyEnv}` for any number of endpoints (vLLM, llama.cpp server, LiteLLM, and so on) |
| OpenRouter | **exists** (OpenAI client, `OPENROUTER_API_KEY`) | move onto the generic type |
| AWS Bedrock | **exists** (`bedrock.go`, standard AWS credential chain). Upstream lists Claude 3.7 Sonnet only | model discovery and more models |
| Ollama / LM Studio | **partial** (`LOCAL_ENDPOINT`; fixed 2026-11-24) | first-class `ollama` and `lmstudio` types with auto-probe (item 2), context-window check, and an embedding-model filter |
| xAI, Gemini, Vertex AI, Azure OpenAI, Groq | **exist** | keep |
| Mock (deterministic, offline) | **exists** (`mock.go`, `RSCH_MOCK_RESPONSE`) | use it in the benchmark harness and CI-free tests |
| Routed and fallback providers | **exist** (`routed.go`, `fallback.go`, with tests) | expose them in config, so you can, for example, route cheap tasks to local and fall back to hosted |
| Health checks | **partial.** `rsch doctor` checks that credentials are *present*, not that the endpoint answers | `rsch doctor --providers`: list models, run a one-token completion, measure latency, and check context size |
| Per-provider token accounting | **partial.** `rsch cost` shows persisted session totals; nothing per provider or per turn | per provider, model and turn: prompt, completion, cached and tool-definition tokens, from provider-reported `usage` where available |

**Provider-layer roadmap:**

- **P1.** Define one `Provider` interface that every backend implements: `Models()`, `Health()`, `Send`/`Stream`,
  and `Usage` reporting. Today each backend has its own client, wrapped by a generic `baseProvider`.
- **P2.** Add the generic OpenAI-compatible type, plus Ollama and LM Studio presets on top of it.
- **P3. Done (aa51c6b).** RSCH no longer reads another tool's stored Copilot credentials
  (`~/.config/github-copilot/*.json`), and a generic `GITHUB_TOKEN` no longer enables Copilot. Only an
  explicitly provided token works (`RSCH_COPILOT_TOKEN` or `providers.copilot.apiKey`). The provider is
  documented as experimental and unsupported until GitHub documents a program RSCH qualifies for.
- **P4.** Health checks and per-provider token accounting (feeds items 1a and 5).
- **P5. Implemented, awaiting live verification.** Official Sign in with ChatGPT for plan usage (`internal/chatgpt`;
  `rsch login chatgpt`, `rsch chatgpt status|models|ask`, `rsch logout chatgpt`). It is tested against a fake
  OAuth server only, and no real account has signed in yet. Next steps: a live check on a Mac, then making the
  ChatGPT plan a provider for the agent loop (Responses API with function tools) and supporting several saved
  accounts. Allowed for open-source, locally run apps; see
  [research.md](research.md#signing-in-with-an-existing-ai-subscription).
- No subscription sign-in for Anthropic, Kiro, Gemini, Copilot or xAI unless their documented policies change.
  Use their API keys, cloud credentials (Bedrock, Vertex) or local models instead.

## Default provider today, and what local-first would take

Checked in `internal/config/config.go` and `internal/llm/models/local.go` on 2026-11-24.

- **Before 2026-11-24:** a local server was used only when `LOCAL_ENDPOINT` was set. Otherwise RSCH picked the
  first hosted provider with credentials (Copilot, Anthropic, OpenAI, Gemini, Groq, OpenRouter, xAI, Bedrock,
  Azure, Vertex AI), and with none it failed with `agent coder not found`.
- **Now:** steps 1, 2, 5 and 6 below are implemented (`internal/config/local_detect.go`, tests in
  `local_detect_test.go`). Verified on the Linux box: with no config and no keys, `rsch doctor` reports
  `provider-in-use: ok (local (auto-detected http://localhost:11434/v1), model llama3.2-3b-16k:latest)` and
  `rsch -p "Reply with the single word OK."` answered `OK` through Ollama. With nothing reachable, doctor
  fails `provider-in-use` with a setup hint. Steps 3 and 4 are still open.
- **The plan:**
  1. When no provider is configured, probe `http://localhost:11434/v1/models` (Ollama) and
     `http://localhost:1234/v1/models` (LM Studio) with a short timeout. Do this lazily in `config.Load`
     rather than with network I/O in a package `init()`.
  2. Skip embedding-only models (for example `nomic-embed-text`) when choosing the default. Today the first
     listed model wins, even if it cannot chat.
  3. Have `rsch doctor` check the server's context window against RSCH's prompt plus tool-definition size
     (~10k tokens) and print the fix (`num_ctx` / `OLLAMA_CONTEXT_LENGTH`).
  4. Prefer a tool-capable default model, and document a recommended size. 3B models loop (item 4).
  5. Keep hosted providers opt-in. A hosted provider is used only when its key is set, and explicit
     `agents.*.model` config always wins.
  6. Add tests for: no endpoint and no keys (clear error with a local setup hint), local only, and local
     plus keys (local wins unless configured otherwise).

## Distribution plan: releases and Homebrew

This is a plan only. **No tags, releases, tap repository or Homebrew PRs have been created.** Each step
needs Surya's go-ahead.

1. **Real GitHub Releases, built locally.** GitHub Actions is not available for this account, and the local
   `gh` login lacks the `workflow` scope, so CI cannot publish. Run GoReleaser locally from
   `.goreleaser.yml` for darwin arm64 and amd64, plus linux amd64 and arm64. Then publish the archives,
   `checksums.txt` and the SBOM to a GitHub Release for an immutable tag (for example `v0.1.0`). Prerequisite:
   fix the module path (item 11) or keep the `-X github.com/opencode-ai/opencode/internal/version.Version`
   ldflag consistent with it.
2. **Bridge: a personal tap.** Create `RamSuryaCH/homebrew-tap` with a `Formula/rsch.rb` that builds from
   the tagged source archive (`depends_on "go" => :build`, `go build`, SHA-256 of the archive) and has a
   `test do` block running `rsch --version`. Users would then run `brew install RamSuryaCH/tap/rsch`. Checked
   2026-11-24: the tap repository does not exist yet.
3. **Later: homebrew-core, so a plain `brew install rsch` works.** Requirements from
   [Acceptable Formulae](https://docs.brew.sh/Acceptable-Formulae) and the
   [Package Acceptance Policy](https://docs.brew.sh/Package-Acceptance-Policy):
   - A stable, immutable tagged release, with a build-from-source formula verified by SHA-256.
   - The build must pass on Homebrew's macOS and Linux CI matrix, along with `brew audit --new --strict`
     and `brew test`.
   - A DFSG-compatible license. MIT qualifies.
   - A separately named fork must be clearly distinguished from the original. RSCH's name and NOTICE do this.
   - **A free name.** Checked 2026-11-24: there is no `rsch` formula in homebrew-core
     (`Formula/r/rsch.rb` returns 404, as does the formulae.brew.sh API) and no `rsch` cask.
   - **Notability.** Normally at least 30 forks, 30 watchers or 75 stars. A **self-submission by the
     repository owner needs at least 90 forks, 90 watchers or 225 stars.** As of 2026-11-24 the repo has
     0 stars, 0 forks and 0 watchers, so self-submitted new projects like this are usually rejected until
     they have users.

Each increment must ship with tests, pass `go test ./...`, and be described here only as far as it has been verified.
