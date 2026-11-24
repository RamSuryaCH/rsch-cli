# RSCH runtime boundaries

## Durable events and SSE replay

The application event bus carries versioned `events.Event` values to live
subscribers and registered sinks. The application installs the SQLite event
log as a sink before constructing agents and tools, so workflow, tool, model,
sandbox, hook, and lifecycle events share one append-only evidence stream.

Event data is recursively redacted at creation and again at persistence. A
sink failure is observable through the publish result but does not drop live
subscribers. The HTTP stream at `/v1/events` accepts `after=<sequence>` for
bounded catch-up before waiting for new events. The local SQLite log remains
the durable source of truth; SSE is a delivery view.

Typed pub/sub delivery is also bounded and non-blocking. Broker constructors
apply their configured queue limits, and `Broker.Stats` exposes subscriber,
published, delivered, and dropped-delivery counts so a slow TUI or other
interactive consumer is observable without turning event publication into a
blocking workflow dependency.

## Recovery and replay safety

`rsch recover <task-id> --json` analyzes persisted lifecycle events and returns
an incomplete/failed action plan. Persisted events contain action identity and
outcome evidence, never raw command arguments. Re-execution therefore requires
the caller to provide fresh requests. The recovery service sends each supplied
request through the normal execution executor, including authorization, risk,
approval, timeout, sandbox, event, and evidence checks. A replay record is not
an authorization to repeat an action.

## Plugins

Plugin manifests use API version `v1` and are validated for known capabilities,
non-negative budgets/timeouts, non-empty permissions, and stable identifiers.
`plugin.Descriptor` exposes sorted capabilities, permissions, and extension
declarations for inspection. Runtime execution is delegated to the governed
project runtime; plugins do not receive a private process or permission
boundary.

## Retry and timeout semantics

Scheduler retry limits count retries in addition to the initial attempt, while
reported `Attempts` is one-based. Every attempt receives a fresh timeout
context, and cancellation propagates through the runner. Specification plans
retain the latest command evidence per task and attach the scheduler's total
attempt count, so a retry does not create duplicate task results.

## Process lifecycle

Authorized argv execution is centralized in `internal/process`. One-shot
commands use `RunOutput`; persistent jobs use `Start` and `Handle.Wait/Kill`;
streaming protocol processes use `StartPipes`. Platform adapters own process
groups, liveness, interrupts, forceful termination, environment construction,
and exit-code behavior. On Windows, normal processes are attached to a native
Job Object with kill-on-close descendant cleanup; explicitly detached jobs use
a non-killing Job Object so the launcher can exit safely. Background jobs
persist only the PID and redacted metadata, then use the same adapter for
stop/recovery checks. LSP, the
persistent shell, search/completion pipelines, and the TUI editor all use this
boundary; no shell string is introduced by the process package.

Linux filesystem-isolated execution requires a functional bubblewrap backend.
The firejail adapter does not meet the workspace filesystem-isolation contract
and cannot authorize isolated execution. Sandbox processes inherit only `PATH`
from the host environment, plus environment variables explicitly supplied by
the caller; provider credentials are not inherited implicitly.

## Evaluation telemetry

`rsch eval` runs reproducible tests, static analysis, and the bounded local
secret scan. When the workspace database exists, it also samples persisted
session token/cost dimensions, lifecycle-event counts, and explicit permission
decision events. Permission requests and grants/denials are emitted on the
same durable bus used by SSE and TUI consumers. Missing dimensions remain
explicitly marked unavailable; the evaluation never infers values from elapsed
time or log text.
