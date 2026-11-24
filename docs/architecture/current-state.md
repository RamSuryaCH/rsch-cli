# RSCH current-state architecture

This checkout is a Go 1.26 terminal client descended from the SST OpenCode
repository. The executable starts a Cobra command, loads Viper configuration,
opens SQLite, constructs `internal/app.App`, and runs either the Bubble Tea TUI
or a non-interactive prompt. Providers and tools are behind Go interfaces and
the application publishes typed events to the TUI.

## Existing capabilities

- Provider-neutral model selection for OpenAI, Anthropic, Gemini, Bedrock,
  Azure, Copilot, Groq, OpenRouter, Vertex AI, xAI, and local models.
- SQLite-backed sessions, messages, and file history with migrations.
- Resumable sessions are also exposed through authenticated HTTP resources for
  list/create/detail/delete operations and per-session model-usage telemetry.
- Permission prompts, MCP tools, shell/file/search tools, LSP integration,
  cancellation contexts, and structured text/JSON output.
- A reusable pub/sub layer and a TUI that consumes application events.
- RSCH workflow services: goals, requirements, task DAGs, evidence, durable
  event replay, HTTP/SSE endpoints, verification, review, checkpoints, and
  repository-aware interviews. Non-interactive natural-language runs create a
  durable goal/task lifecycle, persist success/failure evidence, and are
  recoverable through the existing workflow/session inspection surfaces.
- Durable agent lifecycle records with restart recovery, executable
  specification plans and traceability/drift reports.
- Persisted agents can be created, inspected, transitioned, retried, and
  recovered through the `/v1/agents` API and `rsch agents` lifecycle commands;
  every transition is validated by the agent state machine and appended to
  the durable event log.
- Agent records persist the full execution contract (skills, tools,
  permissions, worktree, tokens, cost, duration, changed files, errors, and
  verification state). Structured findings, warnings, dependencies, handoffs,
  artifacts, questions, statuses, and results can be published as versioned
  `agent.message` events.
- Sub-agent execution applies configurable task-agent token budgets and
  timeout limits (`agents.task.maxTokens` / `agents.task.timeoutMs`) to the
  actual agent context.
- Executable specification plans retain DAG scheduling and retries while
  sending each command through the shared risk, command-allowlist, timeout,
  cancellation, and evidence execution boundary.
- Background jobs and scheduled tasks preserve explicit timeout/state
  transitions, reject terminal-state overwrites, and clean up interrupted
  processes where the platform permits. Persisted prompts, terminal errors,
  and completed job logs are redacted before they become durable inspection
  state.
- Capability-governed project resource execution and platform sandbox
  adapters for macOS/Linux, with fail-closed behavior when isolation is not
  available.
- Dependency intelligence for Go module graphs and npm/Python manifests,
  exposed through `rsch dependencies`; discovery is recursive, reports
  package/manifests for monorepos, and maps Git changes to affected packages
  when Git inspection is available.
- Read-only SQLite schema intelligence is available through `rsch database`
  and `/v1/workspace/database`, including table/column metadata, foreign keys,
  indexes, and migration-file table provenance without executing arbitrary SQL.
- Infrastructure intelligence for Docker, Compose, Terraform, and Kubernetes,
  exposed through `rsch infrastructure`, plus reusable performance budgets,
  bounded filesystem I/O measurements, SQLite pool evidence, bounded query-plan
  sampling, bundle inspection, and explicit configured-budget validation exposed
  through `rsch performance`.
- Scoped project memory is persisted atomically under `.rsch/memory` and is
  inspectable through `rsch memory add/list/search/remove/explain`.
- WebDriver browser sessions support navigation, CSS element lookup, click and
  typing actions, DOM and console inspection, script evaluation, and
  screenshots. The coder agent exposes these operations through a persistent,
  permission-gated browser tool; image evidence analysis, persisted sessions,
  isolated Git experiments, and detached background jobs are exposed as
  explicit capability boundaries with failure/recovery state.
- Browser and HTTP policy validation rejects private or unresolved hostnames
  when private access is not explicitly enabled, including redirect checks;
  persistent shell command framing uses native POSIX, `cmd.exe`, or PowerShell
  syntax according to the configured platform shell.
- Plugin manifests expose versioned extension descriptors, and named plugin
  capabilities are invoked only through the shared project runtime after
  declaration and governance checks.
- The `view` tool returns verified image evidence through the visual decoder
  instead of treating images as unsupported text files.
- `rsch architecture render` produces a bounded PNG architecture overview
  from the repository dependency graph and records it as visual evidence;
  `rsch analyze` remains available for supplied screenshots and images.
- Experiment comparisons now report bounded test, build-performance, secret
  scan, changed-file/complexity, and cost-telemetry availability evidence;
  unavailable evidence is labeled rather than inferred, and recommendations
  are conditional or negative until the required dimensions are observed. A
  Git-unavailable comparison is persisted as unavailable evidence instead of
  disappearing from the confidence record.
- Model routing supports task classes (`fast`, `coding`, `reasoning`, `vision`,
  `review`, and `security`), capability-aware configured candidate ordering at
  request time, ordered per-agent fallback models, partial-stream discard,
  provider/model transition provenance, and performance/cost budgets; release
  checks persist build/runtime evidence per target under
  `.rsch/release/matrix.json`.
- Cost inspection aggregates persisted session cost, input/output tokens, and
  assistant-model counts when message evidence is available; detailed model,
  provider, task, duration, and cost observations are persisted separately and
  unavailable dimensions remain explicitly labeled. Session and task budget
  ceilings are enforced after model responses, and API/CLI cost views expose
  configured limits with per-session and per-task pass/fail checks.
- A callback-backed execution boundary used by read-only task-agent tools;
  legacy mutating tools now pass through a policy gate at the permission
  service seam while retaining their existing user approval UI. Governed tool
  descriptors expose machine-readable input/output, permissions, risk,
  timeout, execution mode, and capabilities metadata.
- Permission decisions are first-decision-wins and non-blocking under duplicate
  or concurrent UI responses; cancellation and the bounded prompt timeout
  leave no indefinitely pending request.
- Documentation scanning parses Markdown headings, links, and image references,
  skips code examples and symlinked documents, and confines local link checks
  to the workspace. Individual documents are limited to 8 MiB; `rsch docs
  check` maps changed paths to documentation obligations and records the
  assessment in the redacted append-only audit log.
- `rsch review` performs independent deterministic assessments for patch
  integrity, tests, static architecture checks, changed-file security,
  maintainability, regression risk, performance timing, and feature-registry
  compliance; unavailable dimensions remain explicit, and Git absence is
  recorded as an unavailable assessment rather than preventing report
  persistence. `rsch review --snapshot` is an explicit non-Git mode that
  inventories supported workspace files, runs the same applicable checks, and
  reports `scope=workspace-snapshot` without claiming diff integrity.
- The TUI exposes a live workflow page on `Alt+W`; CLI/API surfaces include
  `explain`, `why`, `search`, `context`, configuration hierarchy inspection,
  task state operations, top-level task create/edit/transition resources,
  agent lifecycle controls, smart-commit preview, and reproducible `eval`.
- Bounded context retrieval reports source-match provenance plus available
  architecture, specification, memory, steering, skills, tools, and Git
  context layers under a shared token budget.
- The HTTP service exposes read-only workspace resources for agents, memory,
  steering, browser capability, jobs, cost, audit, security, and release
  evidence. Tasks also have a client-independent collection/resource API for
  create, list, edit, and validated transitions.
- The HTTP service also exposes structured agent messages and replay detail;
  replay inspection groups goals, tasks, agents, messages, tool actions,
  failures, repairs, verification, and the final event. Background jobs expose
  per-job log, stop, and recovery operations through the same policy boundary.
- The HTTP service supports optional bearer-token authentication; the CLI
  requires `RSCH_SERVER_TOKEN` for non-loopback binds and leaves only the
  minimal `/healthz` endpoint unauthenticated. Configured workflow mutations
  pass through the shared execution policy. Policies that require interactive
  approval fail closed over HTTP; callers cannot self-approve with a header.
- Architecture graphs and reverse dependency impact reports are persisted
  under `.rsch/intelligence`; background jobs emit redacted lifecycle
  notifications under `.rsch/notifications`.
- The event bus can persist the application-wide lifecycle stream to SQLite;
  `rsch recover <task-id> --json` derives a safe plan of incomplete and failed
  tool actions. Re-execution requires caller-supplied requests and delegates
  back through the execution policy, approval, risk, timeout, and sandbox
  boundary; persisted events never contain raw command arguments.
- Notifications can additionally fan out to an explicitly configured webhook
  only when its host is network-allowlisted, or to an exactly command-allowlisted
  non-shell executable inside an OS sandbox. Missing policy or isolation fails
  closed; local persistence remains the durable source of truth.
- Security inspection combines deterministic local secret detection with
  explicitly requested `govulncheck`, `npm audit`, `pip-audit`, and `trivy`
  adapters. Their vendor reports are normalized into structured package,
  advisory, severity, message, and fix fields. Missing tools, inapplicable
  manifests, non-zero exits, and output are recorded as redacted, structured
  observations; no external scanner is launched implicitly.
- The interactive TUI subscribes to the same versioned event stream and
  surfaces failure, policy, sandbox, security, and Git-conflict events while
  retaining its existing typed session/agent subscriptions.
- Permission requests and explicit grants/denials are also durable event
  records, allowing evaluation to report observed human intervention without
  fabricating it when the event store is unavailable.
- Workflow goals, requirements, evidence, agent metadata, background prompts,
  and terminal job state apply credential redaction at their persistence or
  inspection boundaries; detached process file descriptors remain native so
  background jobs are not converted into launcher-owned pipes.
- Governance allowlists are enforced for configured models and MCP servers.
  Project resource execution always requires a kind-scoped approval and an OS
  sandbox; repository hooks are activated only for explicitly trusted workspaces.
- `rsch team` and `/v1/workspace/team` inventory shared agents, skills,
  steering, hooks, policies, specs, and memory resources without claiming
  resources that are absent.
- The policy boundary derives explainable risk when callers omit a risk level;
  execution and performance-budget outcomes can emit redacted notifications.
- `rsch performance --json` and the HTTP performance resource expose bounded
  CPU, memory, disk, process, network, token, cost, I/O, and database evidence;
  dimensions unavailable on the host are explicitly marked unobserved. Unix
  process counts use procfs; Windows uses `tasklist`, and Windows disk
  capacity uses `GetDiskFreeSpaceEx`.
- Authorized one-shot and streaming processes run through `internal/process`,
	which gives Git, verification, dependency, release, resource, local sandbox,
	LSP, shell, search, completion, editor, and background paths consistent argv
	construction, explicit environments, exit codes, pipes, and process-group
	cancellation. Specialized adapters retain protocol framing and terminal UX,
	while process creation and lifecycle stay centralized.
- Sandbox capability records distinguish support, filesystem, network, and
  process isolation. Windows uses native Job Object process containment and
  requires a configured wrapper for filesystem/network isolation; resource
  execution remains fail-closed when the complete boundary is unavailable.
- `rsch release check-all` records the six declared target combinations;
  `rsch release targets` advertises only combinations with persisted
  successful build evidence. Current host evidence shows all six
  cross-build successfully. Darwin/arm64 is runtime-tested with an injected
  probe version that must exactly match the binary's final version line; the
  five non-native targets remain build-only.
- Analysis and verification producers persist redacted, timestamped evidence
  records under `.rsch/evidence`; `rsch evidence` and
  `/v1/workspace/evidence` expose the same records. The visual, dependency,
  infrastructure, performance, browser, and master-specification audit
  commands all publish through this boundary.
- `rsch spec audit <master-prompt>` parses numbered specification sections,
  maps candidate registry features, reports concrete verification references,
  and persists an explicit gap report under `.rsch/requirements/audit.json`.
- `rsch confidence` and `/v1/workspace/confidence` calculate a weighted
  confidence score only from observed evidence. Unobserved dimensions reduce
  coverage and cannot pass silently.
- Evidence records can be linked immutably to goals, tasks, and requirements
  with `rsch evidence link/links` and `/v1/workspace/evidence/links`, keeping
  workflow traceability separate from the append-only observation log.
- `rsch fork <id>` preserves a dirty parent by copying tracked diffs and
  untracked files into an isolated detached worktree; the parent is never
  reset or staged.
- `rsch goal list` and `rsch goal show <id>` expose persisted goal plans,
  requirements, task states, and command metadata for resumable inspection;
  `rsch agents show <id>` and `GET /v1/goals/<goal-id>/tasks/<task-id>` expose
  durable agent/task detail, including task timeout and retry metadata.

## Gaps against the product specification

The remaining product-scale gaps include native runtime validation on every
target, richer external scanner integrations, and human-intervention/model-
level evaluation telemetry when those dimensions are available. These must be
introduced behind interfaces rather than embedded in the TUI.

## Risks and constraints

- The existing module path and some internal identifiers are upstream names;
  changing them all at once would break imports and provenance.
- Some read-only legacy paths outside task-agent orchestration still need to
  be migrated to the callback boundary before autonomous execution can claim
  complete centralization.
- OS sandbox backends differ materially; capability detection and fail-closed
  behavior must remain authoritative rather than advertising unsupported
  isolation.
