---
name: rsch-writeback
description: Report RSCH results and defects with reproducible, secret-safe evidence.
---

# Writeback and feedback

Return results to the requesting user or the repository's existing issue or
review channel. RSCH does not currently provide a general `issue` subcommand,
so do not invent or advertise one.

For a successful task, include:

- the outcome and changed paths;
- the exact verification commands that passed;
- any checks not run and why;
- persisted evidence or task identifiers when the command produced them.

For a defect, include:

- `rsch --version`, platform, and the relevant command;
- the working directory or a sanitized description of it;
- exact arguments with secrets replaced by placeholders;
- exit status and the structured stderr `code`, `message`, and `suggestion`;
- expected behavior, actual behavior, and a minimal reproduction;
- whether the problem is deterministic and any safe workaround.

Never write API keys, authorization headers, environment dumps, private source
text, or unredacted model/tool output into memory, research records, logs, or
issue reports. Attach only the minimum evidence needed to reproduce the result.
