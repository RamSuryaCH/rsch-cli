# RSCH feature matrix

| Capability | Current checkout | Initial RSCH implementation | Remaining work |
| --- | --- | --- | --- |
| Provider-neutral models | Implemented | Preserved | Broader provider health and native runtime evidence |
| Sessions and messages | Implemented | Preserved with HTTP session lifecycle, usage resources, and workflow-page session summaries | Additional session detail controls |
| Permissions | Implemented | Preserved | Continue auditing legacy tool paths; concurrent decisions are bounded and first-decision-wins |
| MCP and tools | Implemented | Preserved | More provider-specific sandbox evidence |
| Unified tool contracts | Implemented | Governed tool descriptors now expose schemas, permissions, risk, timeout, execution mode, and capabilities | Provider-specific output schemas |
| Goals/requirements/tasks | Implemented | `internal/engineering` domain core plus durable prompt-run lifecycle, task command/timeout/retry metadata, goal/agent inspection, validated agent lifecycle API/CLI operations, and top-level task create/edit/transition resources | Richer visual workflow controls |
| Verification/evidence | Implemented | Evidence contract plus durable cross-surface store, explicit external scanner observations, and normalized vendor findings | Wider scanner coverage |
| TUI | Implemented | RSCH workflow surface, durable-event failure feed, and validated task/agent lifecycle controls | More visual workflow views |
| HTTP/SSE API | Implemented | Client/server service boundary, structured message and replay-detail APIs, task collection/resource editing, per-job log/stop/recovery operations, and policy-governed mutations with explicit approval headers | Additional specialized resources |
| Repository intelligence | Implemented | Incremental index, architecture graph, provenance-aware retrieval, and deterministic relevance ranking | Larger-repository tuning and optional model-backed embeddings |
| Visual intelligence | Evidence-backed | Deterministic screenshot analysis plus labeled, repository-derived architecture PNG rendering with persisted visual evidence | Browser-captured UI screenshots require an enabled WebDriver |
| Cross-platform release | Evidence-backed | Build matrix and artifacts | Validate every target on native CI runners |
| Dependency intelligence | Implemented | Manifest and graph inspection | More manager-specific audit adapters |
| Monorepo support | Implemented | Recursive Go/npm/Python manifest discovery with provenance, Git affected-package mapping, and bounded execution plans | More manager-specific audit adapters |
| Database intelligence | Implemented | Read-only SQLite schema, foreign-key/index, and migration relationship inspection with CLI/API surface | Query-plan instrumentation |
| Performance intelligence | Implemented | Runtime, resource, budget primitives, SQLite statement/profile tracing, bounded read-only query-plan sampling, bundle inspection, and explicit configured-budget validation | Broader host telemetry and native provider instrumentation |
| Evidence aggregation | Implemented | `rsch evidence`, immutable workflow links, `/v1/workspace/evidence`, `/v1/workspace/evidence/links`, and `rsch spec audit` | Richer experiment comparisons |
| Confidence engine | Implemented | Evidence-weighted `rsch confidence` and workspace API | Broader independent reviewer/model evidence |
| Notifications | Implemented | File-backed event provider | Operational delivery guarantees and provider health |
| Authorized process execution | Implemented | Platform-aware argv/env/timeout/pipe lifecycle boundary, Unix groups, Windows Job Objects | Native runtime validation on every target remains |
| Browser agent | Implemented | Permission-gated coder-agent browser tool over policy-validated WebDriver sessions with navigation, DOM, console, screenshot, element, interaction, and script APIs | Native WebDriver runtime evidence requires an external browser service |
| Agent observability and communication | Implemented | Durable agent contract metadata, model usage records, and structured `agent.message` events | Broader external telemetry integrations |
| Cost and budgets | Implemented | Model/provider/task/session usage aggregation, task/session budget enforcement, and API/CLI budget checks | Provider-specific price catalog maintenance |
| Security findings | Implemented | Redacted secret findings plus explicit `govulncheck`, `npm audit`, `pip-audit`, and `trivy` adapters with normalized findings and fail-closed availability reporting | Wider scanner coverage |
