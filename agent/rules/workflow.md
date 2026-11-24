---
name: rsch-workflow
description: Use RSCH through a conservative inspect, act, and verify workflow.
---

# Operating workflow

1. Set the workspace explicitly with `--cwd <path>` when the current directory
   may be ambiguous.
2. Discover the live interface with `rsch --help` and
   `rsch <command> --help`. Inspect repository instruction precedence with
   `rsch instructions explain . --json`.
3. Establish a read-only baseline:

   ```sh
   rsch status --json
   rsch doctor --json
   rsch config validate
   ```

4. Gather focused context before asking a model to act:

   ```sh
   rsch search "query" --json --limit 20
   rsch context "query" --json --budget 4000
   rsch explain path/to/file --json
   rsch impact path/to/changed-file --json
   ```

5. Choose an execution mode deliberately:

   ```sh
   rsch                                      # interactive TUI; requires a TTY
   rsch --prompt "Summarize this repository" --output-format json --no-input
   printf '%s\n' "Summarize this repository" | rsch --output-format json
   ```

   In non-interactive mode, tool approvals are denied unless the caller
   explicitly opts into `--auto-approve` or `--yes`. Do not add either flag on
   an agent's own initiative.

6. Review changes and evidence. Prefer `rsch git status --json`,
   `rsch git diff`, `rsch review --json`, and focused package tests. A plain
   `rsch verify --json` reports configured verification checks; a failing or
   unavailable required gate is not success.
7. Report commands run, exit status, files changed, remaining risks, and any
   skipped verification. Never infer completion solely from a model response.

Most commands still default to human text. Use a command's local `--json` flag
when present; the root `--output-format json` applies to prompt mode, not every
subcommand.
