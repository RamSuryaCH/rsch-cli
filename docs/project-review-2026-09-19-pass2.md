# Second project review — 19 September 2026

All 32 specialist agents reviewed their assigned areas again, preserved the first pass, and implemented additional demonstrated fixes. This pass closes the actionable backlog from the first review and addresses new defects found in persistence, policies, cancellation, redaction, parsers, tools, and terminal behavior.

The minimum supported Go version is now **1.26.0**. CI uses Go 1.27.0 and includes a separate Go 1.26.0 compatibility job with automatic toolchain upgrades disabled. This permits the published dependency fixes in gRPC v1.83.2, x/crypto v0.56.0, and x/net v0.58.0.

The original 509 files at the start of this pass are preserved in `.codex/project-review-20260919-pass2/source-before.tar.gz`. Its SHA-256 manifest, incremental `changes.patch`, test inventory, and verification logs are in the same directory. The checkout still has no Git metadata. No release or deployment was published.

## First-review backlog

| Finding | Result in this pass |
| --- | --- |
| Task updates and event persistence were separate; multiple servers could lose updates | Built-in SQL stores now read, mutate, and append the event in one serializable transaction. Cross-server regressions cover concurrent updates and rollback for all six task mutation routes. |
| Evaluation and experiments silently skipped inputs | Shared bounded scanner reports coverage and reasons. Unreadable, oversized, nonregular, and excess inputs prevent a complete pass or merge recommendation. Partial findings remain visible. |
| LSP string/zero IDs and watcher failures | Request IDs preserve their wire representation, watcher failures return safely, matching watch kinds combine, and ripgrep no-match completion is empty success. |
| Large notification records broke history | Streaming decoding reads accepted records; rooted storage rejects symlink escapes. |
| LS tree and architecture `other` grouping | LS starts at the requested directory and reports truncation correctly; architecture overflow cannot collide with real module names. |
| Copilot refresh ignored cancellation | Refresh HTTP calls and queued refresh waits honor caller cancellation. |
| Four non-reachable dependency advisories | Patched gRPC and x/crypto issues. The final scan reports no reachable vulnerabilities or vulnerable imported packages. One advisory remains solely in the unimported upstream OpenPGP package, which has no fixed release. |

## Additional fixes

**Workflow and execution integrity.** Replacing tasks preserves evidence for retained task IDs instead of deleting and recreating every task. State and events roll back together if event persistence fails. Domain mutation/broadcast APIs avoid duplicate primary-log append and preserve replay sequence identity. Unknown autonomy/risk values and missing governed executors fail closed. Policy decisions use canonical identities; permission cancellation cannot emit conflicting terminal decisions, and persistent grants produce audit events.

**Cancellation and resource cleanup.** Process failures preserve cancellation/deadline causes alongside exit status. Native macOS Seatbelt tests cover symlinked workspaces and protected control directories. Background job admission is serialized across managers/processes and honors cancellation. Agent streams preserve completion-only text, count cached reads as input, and stop on MCP cancellation/denial. Copilot, ordinary provider streams, timeout wrappers, browser queues, Git commits, experiment commands, specifications, context retrieval, and explanations now propagate cancellation in the reproduced cases. Failed browser deletion retains state for retry and prevents restart from abandoning the old session.

**Data and configuration.** Invalid configuration is not cached after a failed load. Atomic writes preserve unknown fields, trust settings, explicit false values, large JSON integers, and unrelated MCP settings. Omitted token limits use defaults without misleading warnings; negative limits remain invalid. Title-agent timeouts survive validation and failed persistence does not activate a theme. History and message bulk deletion is atomic. Message strings are redacted before serialization, preserving binary parts and caller-owned data. Research deduplication respects polarity and source identity; withdrawn memories no longer suppress current facts through obsolete conflicts. Steering and trace inspection reject external symlink targets.

**Credential and terminal safety.** Key-aware log redaction protects both debug files and the TUI. Scanning/redaction handles quoted JSON keys, escaped values, unquoted assignments, authorization headers, incomplete private keys, and long lines. Markdown filtering also runs after entity decoding, preventing decoded control characters from reaching the terminal. Edit/write approval paths revalidate canonical destinations before mutation. Checkpoint restore rejects redirected paths, and checkpoint/worktree storage and copies stay inside their intended roots.

**Providers and tools.** Empty Google provider initialization returns an error instead of a nil client. Anthropic tool argument fragments are decoded once. Truncated OpenAI/Copilot streams cannot complete partial tool calls. Gemini cached tokens are excluded from ordinary input counts to avoid double billing/accounting. Sourcegraph errors no longer appear successful, result limits are honored, and invalid line numbers cannot panic. File/glob/grep paths preserve newline filenames, global newest-file ordering, limits, and cancellation. Patch parsing terminates on unknown directives and rejects invalid chunk bounds instead of hanging or panicking.

**Inspection and evidence.** CLI inspection respects selected output streams, reports write errors with the established envelope, and avoids storage initialization on canceled session operations. Non-ready Git review reports return failure. Recovery orders actual lifecycle events and validates the entire replay plan before resolving or executing actions. Unstarted verification checks report exit code -1; failed checks preserve real exit codes and gate confidence is computed. Workflow evidence is persisted before tasks are marked verified, and output/evidence failures remain visible.

**Terminal usability.** Dashboard selection stays visible and follows task identity across refreshes. Chat headers receive current session updates. Log details follow the selected record, preserve it when new logs arrive, reset scroll on selection changes, and wrap long lines. Editor theme changes preserve cursor/focus. Completion replaces the token at the cursor, handles Unicode and repeated tokens, preserves following text, and ignores stale results.

**Repository resources and release tooling.** Skills validate instructions/metadata before publishing; search is case-insensitive and bounded. Plugins and custom agents reject unusable fields and negative limits. Dependency reports preserve npm's normal exit-1 outdated results and exact Git filenames/subdirectory roots. WebDriver evaluation uses valid empty argument arrays. Infrastructure/migration scanners reject symlink escapes and parse Docker/Terraform labels correctly. Dependency tests use local fixtures instead of package-manager network calls. Cached installations still configure shell/CI PATH, and fixture tests isolate CI environment writes.

## Verification

This pass changes 139 existing files and adds 32 files, including this report. It adds **177 test functions** compared with the preserved baseline. Package owners reproduced defects with failing regression tests and ran focused checks; the following aggregate checks passed after integration.

| Check | Result | Evidence file |
| --- | --- | --- |
| `GOMAXPROCS=4 go test -p 2 ./...` | 83 packages passed; 5 packages have no tests | `settled-tests.log` |
| `GOMAXPROCS=4 go test -race -p 2 ./...` | 83 packages passed; no reported data races | `settled-race.log` |
| `GOMAXPROCS=4 go vet -p 2 ./...` | Passed | `settled-vet.log` |
| `GOTOOLCHAIN=go1.26.0 GOMAXPROCS=3 go test -p 2 ./...` | Minimum-version compatibility passed for all 83 tested packages | `settled-go126.log` |
| CGO-disabled builds for Linux, macOS, and Windows, each on amd64 and arm64 | All six builds passed | `cross-builds.json` |
| `govulncheck` v1.1.4 | Zero reachable vulnerabilities and zero vulnerable imported packages; one module-only OpenPGP advisory remains | `govulncheck.log` |
| CLI end-to-end smoke suite | Passed | `e2e.log` |
| Installer and release-archive fixture suites | Passed | `install-tests.log`, `release-tests.log` |
| Agent asset verification | Passed | `agent-assets.log` |
| `go mod verify` and `go mod tidy -diff` | Modules verified; no tidy changes | `module-verification.log`, `module-tidy.log` |
| `gofmt -l` on changed/new Go files | No formatting differences | `gofmt.log` |
| Latest binary: `status --json`, `doctor --json`, `config validate`, `instructions explain . --json`, `docs --json` | All exited 0 with empty stderr; JSON outputs parsed successfully; doctor reported zero failures and warnings | `inspection-results.json`, command logs |

Native aggregate tests used macOS/arm64 with Go 1.27.0, plus the explicit Go 1.26.0 compatibility run. All evidence files are under `.codex/project-review-20260919-pass2/`. `change-summary.json` lists the changed files and added test functions; `changes.patch` records this pass against its snapshot, and `final-sha256.json` records the final source checksums. The first-review report and artifacts remain preserved separately.

## Practical limits

- Transactional guarantees apply to the built-in SQL store and adapters implementing the new atomic interface. Legacy adapters retain the previous local locking behavior. An in-memory store cannot share a durable transaction with a separate SQL event log.
- Live event delivery can fail after a durable commit. The committed event remains available through replay; delivery failure is diagnosed without returning a misleading rollback response.
- Go vulnerability advisory GO-2026-5932 describes the unmaintained `golang.org/x/crypto/openpgp` package. RSCH does not import it; it has no published fixed version. The scan reports zero vulnerable imported packages and zero reachable vulnerable symbols.
- Native tests ran on macOS/arm64. Linux and Windows builds establish compilation compatibility; native runtime/sandbox tests on those systems and hosted CI were not executed here.
- Provider tests use local HTTP servers and mocks. Paid/live provider behavior and external deployment were not exercised.
- This is a tested project-wide improvement pass, not a guarantee that every possible defect has been eliminated.
