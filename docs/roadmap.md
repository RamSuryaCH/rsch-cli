# RSCH roadmap

Prioritised by impact (for developer productivity and the pain points in [research.md](research.md)) against
feasibility for a solo maintainer. Status labels: **done** means implemented, tested and verified;
**partial** means it exists but has a known gap; **planned** means not started.
The older slice-by-slice history is in [ROADMAP.md](ROADMAP.md).

| # | Item | Why | Status |
| --- | --- | --- | --- |
| 1 | Local models through `LOCAL_ENDPOINT` (Ollama, LM Studio) | Local-first use and no paid keys | **partial.** Config and reasoning fixes landed 2026-11-24 (c1c047c). The model is reached, but small models loop |
| 2 | Smarter loop guard: identical result, A-B-A-B rotation, failure streaks, a corrective hint to the model before stopping | The most common local-model failure (research §4) | **planned.** Today's guard stops after 3 identical requests |
| 3 | Context and token meter in the TUI and `exec` JSON (prompt, tool definitions and history tokens per turn) | Context and usage exhaustion (research §1) | **partial.** `rsch cost` shows persisted session totals; nothing live per turn |
| 4 | Subagent task tool: a parallel, scoped child session with its own permission profile and a summary returned to the parent | Keeps noisy work off the main context; all three comparison tools have it | **partial.** Only the upstream stateless read-only search agent exists |
| 5 | MCP server mode | Lets editors and other agents reuse RSCH's budgeted context | **done** for 3 read-only tools (10ef2dc). Next: optional `review` and `checkpoint list` tools, and HTTP transport |
| 6 | Agent-skill packaging (`SKILL.md` describing the `rsch search`/`context` CLI) | A lower-context alternative to MCP (research §2) | **planned** |
| 7 | Verify the macOS Seatbelt sandbox on Apple silicon and record the darwin test results | It is a security boundary and is currently untested | **planned.** Needs a Mac |
| 8 | Published releases (goreleaser) and a working install path | Lets people install without Go | **planned.** Blocked on CI/Actions availability. `.goreleaser.yml` exists |
| 9 | Rename the Go module path to `github.com/RamSuryaCH/rsch-cli` | Makes `go install` work and removes the upstream name | **planned.** A mechanical import rewrite across ~500 files |
| 10 | TUI: approval dialog showing the sandbox profile and diff preview; subagent activity panel | Approval fatigue (research §3) | **planned** |
| 11 | Background job stop on Linux | Correctness | **done** (6e20e9b, zombie handling; the test now passes) |
| 12 | IDE integration via ACP | Editor embedding, as in Grok Build | **planned.** MCP server mode covers some of this |

Each increment must ship with tests, pass `go test ./...`, and be described here only as far as it has been verified.
