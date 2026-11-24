---
name: rsch-safe-automation
description: Automate RSCH while preserving explicit approval and machine-readable evidence.
---

# Safe automation

Use `--no-input` for a headless invocation that must not enter the TUI. For
prompt output, select `--output-format json`; for subcommands, use their local
`--json` or `--format json` flag only when help advertises it.

Process-level failures emit a JSON object to stderr with `error`, `code`,
`message`, and `suggestion`. Current exit behavior is:

- `0`: success;
- `1`: command failure;
- `2`: recognized usage or argument error;
- `130`: interrupt or cancellation.

Do not assume every successful command emits JSON, and keep stdout and stderr
separate in automation.

Safety-sensitive operations:

- Prompt tool use is denied by default in non-interactive mode. Never infer
  permission to add `--auto-approve` or `--yes`.
- `rsch checkpoint restore <id>` prints a plan and fails until rerun with
  `--approve`.
- `rsch git smart-commit` previews unless `--commit` is supplied.
- `rsch git pr` previews unless `--create` is supplied.
- Removal commands such as `sessions delete`, `memory remove`, `mcp remove`,
  `plugin remove`, and `skill remove` do not consistently prompt. Obtain user
  confirmation first.
- `rsch recover <task-id>` is inspection-only and does not replay actions.
- Server mutation policies that require approval fail closed; a request header
  cannot self-approve them.

Prefer bounded queries (`--limit`, `--budget`) and retain the exit status,
stderr envelope, and produced evidence identifiers for auditing.
