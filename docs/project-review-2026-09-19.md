# Project review — 19 September 2026

This review used all 32 available specialist agents, with a coordinating integration review, to inspect the RSCH repository and implement concrete improvements. The work concentrated on correctness, security boundaries, persistence, cancellation, terminal usability, and release reliability. It is a substantial hardening pass, not a certification that the project is defect-free or production-ready in every environment.

The checkout has no Git metadata. Before editing, the coordinator saved all 480 original source files and a SHA-256 manifest under `.codex/project-review-20260919/`. That directory also contains the cumulative `changes.patch`, a machine-readable change summary, and verification logs. Existing source was edited in place; no commit, release, or deployment was created.

## Review coverage and implemented improvements

| Review area | Result |
| --- | --- |
| HTTP service | Reject malformed/trailing/null request documents before mutation; prevent status-edit policy bypass; resume SSE from `Last-Event-ID`, replay every backlog page, flush idle connections, and deduplicate replay/live overlap. Serialize task mutations and paginate filtered replay. Log event persistence failures. |
| Execution and permissions | Reject unknown policy decisions; respect cancellation before callbacks or cached/automatic approvals; record failures accurately; match executable allowlists exactly. |
| Sandbox and process lifecycle | Keep sandbox environments explicit; stop processes that exceed output bounds; reject the firejail adapter's unsupported isolation claims. |
| Security, networking, trust | Reject unresolved symlink ancestors, make Trivy findings fail checks, and restore bounded HTTP redirects. |
| File tools | Revalidate patch sources, destinations, and cancellation after approval; correct view pagination, blank lines, Unicode truncation, and long-line handling. |
| Shell and governed tools | Require permission for ambiguous executables, shells, PATH entries, and Git/Go helpers; preserve exact command identities; report model-visible tool errors as failed executions; fail closed when fetch permissions are unavailable. |
| OpenAI-compatible providers | Prevent unintended inheritance of OpenAI credentials/account headers; close streaming responses; unblock canceled streams; reject empty successful completions without panics. |
| Common providers and fallback | Reject incomplete streams; preserve Gemini text parts; handle empty Anthropic histories; make common streams cancellation-aware; preserve eligible provider identity; bound and copy retained fallback payloads. |
| Agent runtime | Interrupt stalled stream receives; contain tool panics; release model capacity after summarizer failures; stop emitting summary success after persistence fails. |
| Prompt, model, routing | Reject nonregular context files; avoid provider calls for canceled requests; retain completed attempts and incurred cost when budgets fail. |
| Application workflows | Deny every headless approval request; align cancellation errors, JSONL status, and durable state; emit terminal task events; preserve exact executable configuration. |
| Configuration and project runtime | Validate and persist model changes before activation, retain timeout/governance settings, confine resource execution, and report actual exit status. |
| Database, sessions, history, messages | Escape SQLite URI paths, order tied history versions deterministically, and delete descendant sessions atomically. Verify foreign-key enforcement survives replacement connections. |
| Engineering and orchestration | Align in-memory/SQL task queries; make replacement atomic; reject stale SQL transitions; enforce capacity on agent retries; drain workers on cancellation. |
| Events, pubsub, audit | Preserve durable publication order; redact nested credential payloads without changing scalar types or numeric precision; read large accepted audit records. |
| Background jobs, hooks, notifications | Confine job metadata/log access, run jobs in the selected workspace, bound tail reads, and cancel/restart hook subscriptions correctly. |
| LSP and completions | Apply UTF-16 positions correctly, preserve newline bytes, accept valid adjacent edits, and enforce resource overwrite/ignore options. |
| TUI shell, pages, layout | Keep containers within allocated dimensions, prevent stale sidebars after session changes, and size argument dialogs when opened. |
| TUI components | Preserve filtered command selection and completion cursor position; fit argument forms to compact terminals and keep focused fields visible; handle empty forms without panics. |
| TUI rendering, themes, images | Bound image decode/preview sizes, confine atomic PNG writes, and make dominant-color ties deterministic. |
| Core CLI | Preserve workspace selection when storage moves; bind IPv6 correctly; shut down the server on cancellation; redact credentials in configuration inspection. |
| Feature CLI | Show external scanner results, reject unknown verification gates, preserve corrupt release evidence, and report release cancellation with correct exit codes. |
| Plugins, skills, custom agents | Confine registry operations, create manifests exclusively, and reject unsafe skill-disable markers. |
| Memory, research, steering | Scope memory exclusion diagnostics; reject invalid research snapshots; preserve valid backups and future schemas; confine research IO and serialize updates across stores/processes. |
| Checkpoints, Git safety, worktrees | Preserve workspace `manifest.json` in checkpoint format v3 with legacy restore support; preserve literal Git filenames; detect nested sensitive paths; handle rename records correctly. |
| Evidence, verification, recovery, requirements | Validate record IDs, make identity publication immutable across concurrent writers and legacy records, and preserve precise JSON numbers. |
| Intelligence, diff, review, risk | Exclude symlink aliases to secrets/ignored targets; review staged and unstaged whitespace; handle unborn repositories and unusual filenames. |
| Specifications, interview, confidence, explain | Validate evidence and task plans consistently; use current evaluation fields; confine explanation and trace inputs. |
| Dependencies, infrastructure, database inspection, browser | Preserve dependency update/audit findings without duplicate enrichment; use W3C typing payloads; handle nullable SQLite schema metadata. |
| Performance, evaluation, experiments, feature registry | Reject nonfinite/overflowing resource usage, preserve build errors with empty output, and require regular in-workspace evidence files. |
| Release, installation, CI | Extract only the expected regular executable, honor the requested install destination, require complete checksum coverage, and retain installer/archive regression suites in Linux/macOS CI. |
| Documentation, format, logging, file utilities | Confine logs and documentation reads, redact private panic reports, parse Markdown links correctly, fix bounded fallback glob traversal/path joining, and align dashboard/sandbox documentation. |

The coordinating scan also identified reachable vulnerability GO-2026-6348 in gRPC. `google.golang.org/grpc` was upgraded from v1.82.1 to v1.83.1, with its required transitive updates; the final scan reports no reachable vulnerabilities. Go 1.25 compatibility is retained.

## Compatibility changes

- Linux isolated execution requires a functional bubblewrap backend. Firejail alone now fails closed because its adapter does not implement the advertised filesystem boundary.
- Sandbox children inherit `PATH` and explicitly supplied environment variables. Callers needing other variables must supply them.
- Executable allowlists preserve exact configured names/paths. A different executable sharing a basename does not inherit approval.
- Git/Go commands, custom or login shells, explicit executable paths, and untrusted PATH entries require normal permission approval. Noninteractive approval remains denied by default.
- New checkpoints use format v3 to separate metadata from workspace files; older formats remain readable.
- New evidence uses immutable ID filenames; legacy timestamp records remain readable and protected from duplicate IDs.

## Verification

All results below passed on the final source and gRPC v1.83.1 dependency graph. The suite contains 81 tested packages and seven packages without tests. This pass added 189 named regression-test functions, many with multiple cases.

| Check | Result | Local log |
| --- | --- | --- |
| `GOMAXPROCS=4 go test -p 2 ./...` | 81 packages passed | `dependency-update-tests.log` |
| `GOMAXPROCS=4 go test -race -p 2 ./...` | 81 packages passed | `dependency-update-race.log` |
| `GOMAXPROCS=4 go vet -p 2 ./...` | Passed | `dependency-update-vet.log` |
| `GOTOOLCHAIN=go1.25.0 GOMAXPROCS=3 go test -p 2 ./...` | 81 packages passed | `dependency-update-go125.log` |
| Linux, macOS, Windows builds on amd64 and arm64, `CGO_ENABLED=0` | All six passed | `final-cross-build-results.json` and `final-build-*.log` |
| `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 -show verbose ./...` | No reachable vulnerabilities; four non-reachable advisories remain, detailed below | `final-govulncheck.log` |
| `bash scripts/e2e.sh` | Passed | `cli-e2e.log` |
| `bash scripts/test_install.sh` | Passed | `install-tests.log` |
| `bash scripts/test_release_archive_checks.sh` | Passed | `release-tests.log` |
| `sh scripts/check-agent-assets.sh` | Passed | `agent-assets.log` |
| Repository Go formatting | No unformatted files | `final-gofmt.log` |
| `status --json`, `doctor --json`, `config validate` | All exited 0; doctor reported zero failures/warnings | `inspection-results.json` and command logs |

An uncached full test/race run also passed before the dependency update. A separate reviewer found no concrete blockers in the combined execution, credential, configuration, and HTTP changes. Native Go runs used macOS/arm64 and Go 1.27.0, plus the explicit Go 1.25.0 run. Intermediate parallel-edit build failures and one interrupted toolchain download were superseded by these successful settled-source checks.

All log paths in this table are relative to `.codex/project-review-20260919/`.

## Remaining follow-ups

These were identified during review and are not claimed as resolved by this pass:

1. **Transactional events and multiple service instances:** task mutation plus event append needs a shared transaction/outbox for guaranteed atomicity. HTTP task serialization currently protects one Server instance; separate service instances/processes need store-level collection concurrency control.
2. **Evaluation completeness:** security scans used by evaluation/experiments need explicit skipped-file accounting so unreadable, oversized, or over-limit inputs cannot be mistaken for complete coverage.
3. **LSP interoperability and watcher recovery:** review string/zero JSON-RPC request IDs, watcher-construction failure handling, and combined registration behavior. Completion fallback should treat ripgrep's no-match exit as an empty result.
4. **Large notification records:** notification history still uses Scanner's default line bound, unlike the repaired audit reader.
5. **Small presentation edge cases:** LS can repeat absolute-path ancestors; architecture grouping's synthetic `other` bucket can conflict with a real module of that name.
6. **Copilot authentication cancellation:** token refresh does not yet carry the caller context and may wait for its bounded HTTP timeout.

7. **Non-reachable dependency advisories:** the final vulnerability scan still lists GO-2026-6443 for an imported gRPC server package and GO-2026-6355, GO-2026-6354, and GO-2026-5932 for unimported SSH/OpenPGP packages in x/crypto. It reports no calls to the vulnerable symbols. Newer gRPC/x/crypto fixes bring a Go 1.26 dependency requirement, so they are deferred to a deliberate minimum-toolchain upgrade. The OpenPGP advisory has no fixed version; the application does not import that package.

Native Linux/Windows runtime and sandbox behavior, live provider integrations, hosted CI, and broader external scanner coverage require separate validation. Cross-compilation does not establish native runtime behavior. The tests in this pass use local fixtures and mock/local HTTP services rather than paid provider requests.
