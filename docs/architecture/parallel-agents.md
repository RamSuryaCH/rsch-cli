# Parallel agent architecture

RSCH uses a coordinator plus narrow specialists. Read-heavy discovery can run
in parallel; implementation runs in parallel only when write ownership is
disjoint. Shared contracts are integrated at checkpoints.

## Contracts

- `internal/events`: versioned operational events; never hidden chain of
  thought.
- `internal/engineering`: durable goals, tasks, DAG dependencies, and task
  states.
- `internal/orchestration`: agent lifecycle, retries, timeouts, recovery, and
  structured handoffs.
- `internal/memory`: scoped, inspectable, removable context records.
- `internal/research`: source/evidence/claim provenance.
- `internal/evidence` and `internal/verification`: immutable observations and
  release-gate status.

## Ownership map

| Workstream | Primary paths | Review gate |
| --- | --- | --- |
| TUI | `internal/tui/` | TUI QA |
| Agents/tasks | `internal/orchestration/`, `internal/engineering/` | Agent reviewer |
| Memory | `internal/memory/`, `cmd/memory.go` | Memory reviewer |
| Research | `internal/research/`, `cmd/research.go` | Research verifier |
| Security | `internal/security/`, security tests | Security reviewer |
| Verification | `internal/verification/`, `cmd/verify.go` | Runtime evidence |

The current checkout has no `.git` directory. Git evidence therefore comes
from an isolated temporary fixture, never from a fabricated claim about this
workspace.

