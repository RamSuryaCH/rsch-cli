# RSCH roadmap

Prioritised by impact (for developer productivity and the pain points in [research.md](research.md)) against
feasibility for a solo maintainer. Core direction: **local-first** (local models by default, hosted
providers bring-your-own-key) and **fewer tokens, more done**. Status labels: **done** means implemented, tested and verified;
**partial** means it exists but has a known gap; **planned** means not started.
The older slice-by-slice history is in [ROADMAP.md](ROADMAP.md).

| # | Item | Why | Status |
| --- | --- | --- | --- |
| 1 | **Always-on automatic token optimiser ("fewer tokens, more done")**: budgeted context and smart file and line selection fed into the agent loop automatically; prompt and context compression; deduplication of repeated file reads and tool results; caching of repeated context; summarising long histories (in `exec` as well as the TUI); trimming tool output by relevance, not just a length cap | The top developer pain point is context and usage exhaustion (research §1, §2) | **partial.** `rsch context --budget` and `rsch search` work (CLI and MCP). Shell output has a 30k-character cap. TUI-only auto-compact is inherited from upstream. Compression, deduplication, caching, agent-loop integration and exec summarisation are **planned** |
| 1a | **Token benchmark**: a fixed task set run with the optimiser on and off, counting tokens with a real tokenizer or the provider-reported `usage` (Ollama returns `prompt_tokens`/`completion_tokens`). Report the task success rate alongside tokens, and publish the harness, raw results and only measured numbers | Proves item 1 rather than claiming it | **planned.** The likely focus of Increment 2 |
| 2 | **Local-first default provider** | Must work with no cloud service or API key | **planned.** See [Default provider](#default-provider-today-and-what-local-first-would-take) |
| 3 | Local models through `LOCAL_ENDPOINT` (Ollama, LM Studio) | Local-first use and no paid keys | **partial.** Config and reasoning fixes landed 2026-11-24 (c1c047c). The model is reached, but small models loop |
| 4 | Smarter loop guard: identical result, A-B-A-B rotation, failure streaks, a corrective hint to the model before stopping | The most common local-model failure (research §4) | **planned.** Today's guard stops after 3 identical requests |
| 5 | Context and token meter in the TUI and `exec` JSON (prompt, tool definitions and history tokens per turn) | Makes item 1 visible (research §1) | **partial.** `rsch cost` shows persisted session totals; nothing live per turn |
| 6 | Subagent task tool: a parallel, scoped child session with its own permission profile and a summary returned to the parent | Keeps noisy work off the main context; all three comparison tools have it | **partial.** Only the upstream stateless read-only search agent exists |
| 7 | MCP server mode | Lets editors and other agents reuse RSCH's budgeted context | **done** for 3 read-only tools (10ef2dc). Next: optional `review` and `checkpoint list` tools, and HTTP transport |
| 8 | Agent-skill packaging (`SKILL.md` describing the `rsch search`/`context` CLI) | A lower-context alternative to MCP (research §2) | **planned** |
| 9 | Verify the macOS Seatbelt sandbox on Apple silicon and record the darwin test results | It is a security boundary and is currently untested | **planned.** Needs a Mac |
| 10 | Releases and Homebrew | Lets people install without Go | **planned.** See [Distribution](#distribution-plan-releases-and-homebrew). Nothing is published |
| 11 | Rename the Go module path to `github.com/RamSuryaCH/rsch-cli` | Makes `go install` work and removes the upstream name | **planned.** A mechanical import rewrite across ~500 files |
| 12 | TUI: approval dialog showing the sandbox profile and diff preview; subagent activity panel | Approval fatigue (research §3) | **planned** |
| 13 | Background job stop on Linux | Correctness | **done** (6e20e9b, zombie handling; the test now passes) |
| 14 | IDE integration via ACP | Editor embedding, as in Grok Build | **planned.** MCP server mode covers some of this |

## Default provider today, and what local-first would take

Checked in `internal/config/config.go` and `internal/llm/models/local.go` on 2026-11-24.

- **Today:** a local server is used only when `LOCAL_ENDPOINT` is set. Its models are discovered at package
  init, and the first listed model (or LM Studio's loaded one) becomes the default for every agent. Without
  `LOCAL_ENDPOINT`, RSCH picks the first hosted provider with credentials in this order: Copilot, Anthropic,
  OpenAI, Gemini, Groq, OpenRouter, xAI, Bedrock, Azure, then Vertex AI. If none is found, `rsch exec` fails
  with `agent coder not found`. **So it is not local-first yet.**
- **What changing it would take:**
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
