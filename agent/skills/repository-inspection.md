---
name: rsch-repository-inspection
description: Inspect a repository with RSCH's read-only context and analysis commands.
---

# Repository inspection

Start with capability and workspace state:

```sh
rsch status --json
rsch doctor --json
rsch instructions explain . --json
```

Use the smallest read-only query that answers the question:

```sh
rsch search "symbol or phrase" --json --limit 20
rsch context "feature area" --json --budget 4000 --limit 20
rsch explain path/to/file --json
rsch architecture --json
rsch architecture render --json
rsch impact path/to/file --json
rsch dependencies --json
rsch docs --json
rsch review --json
rsch security --format json
```

`rsch review` examines changes when Git metadata is available; use
`rsch review --snapshot --json` for a full supported-workspace snapshot without
claiming a Git diff. External security scanners run only when named with
`--external` and depend on locally installed tools and policy.

Results are evidence, not proof of completeness. Repository indexes and
language servers can be unavailable or stale, and commands describe this
checkout rather than external services unless explicitly configured.
