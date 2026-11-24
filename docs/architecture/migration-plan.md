# RSCH migration plan

1. Keep the current Go client/server boundary stable while moving user-facing
   configuration and executable naming to RSCH; retain legacy reads for a
   controlled migration.
2. Add durable workflow tables and an implementation of the engineering store;
   migrate existing sessions independently from workflow state.
3. Introduce a central tool executor with policy, permission, timeout,
   cancellation, audit, and evidence hooks. Route existing shell/file/MCP tools
   through it before enabling parallel agents.
4. Add repository intelligence (file inventory, symbol/dependency index,
   provenance-aware retrieval) as a replaceable service.
5. Add an HTTP API and SSE event stream that consume the same application
   services as the TUI.
6. Add checkpoint, rewind, worktree isolation, verification, review, and
   release adapters only after persistence and audit events are durable.

Each step requires focused unit/integration tests, migration rollback coverage,
and a cross-platform build check before the next subsystem is enabled.
