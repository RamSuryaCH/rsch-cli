# RSCH roadmap

The master specification is a product-scale program, not a single patch. The
repository is being advanced in verifiable slices:

1. Brand/configuration compatibility and client-independent workflow contracts.
2. Durable workflow state, policy-aware execution, audit events, and recovery.
3. Repository intelligence, context provenance, and model routing.
4. HTTP/SSE client boundary and background orchestration.
5. Checkpoints, isolated execution, independent verification, review, and
   cross-platform release validation.

Current implementation also includes persisted agent lifecycle/recovery,
executable specification plans (`rsch spec task add` / `rsch spec run`),
specification traceability (`rsch spec trace` / `rsch spec drift`), governed
project-resource execution, and platform sandbox adapters. The remaining
items are tracked in `.rsch/product/features/registry.json` and the current
state architecture audit.

Documentation intelligence also includes `rsch docs check`, which derives
review obligations from changed paths and records the result in the local
redacted audit stream.

The current slice also includes architecture/impact evidence, background
notifications, governance allowlists, explainable risk assessment, and
approval-gated checkpoint rewind plans. These remain `implementing` in the
feature registry until broader runtime coverage and cross-platform validation
are complete.

“Complete” means an enabled capability has implementation, tests, failure
handling, and observable evidence. Features are not considered complete merely
because a command or documentation entry exists.
