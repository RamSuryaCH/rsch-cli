# RSCH target-state architecture

```text
CLI/TUI and future clients
          │ client boundary
     HTTP + SSE / local API
          │
   session + workflow runtime
   ├─ goals, requirements, specs
   ├─ validated task DAG scheduler
   ├─ agent lifecycle/recovery
   ├─ evidence and verification
   └─ policy/permission gate
          │
   provider-neutral model layer
          │
   tools, MCP, sandbox, repository intelligence
```

The `internal/engineering` package is the first client-independent slice of
this target. It validates task graphs deterministically, models goals and
requirements, and records evidence. Future SQLite/API stores should implement
the same contracts; the TUI must not own workflow state.

All autonomous actions must pass through one policy-aware execution pipeline,
emit an observable event, and produce evidence before a goal can be reported
complete. Unknown platform capabilities and failed security checks remain
explicit failures; they are never silently downgraded.
