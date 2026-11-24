---
name: rsch-trigger
description: Decide when RSCH is an appropriate tool and identify required prerequisites.
---

# When to use RSCH

Use RSCH when the task benefits from repository-aware coding assistance,
including:

- exploring or explaining a codebase;
- searching code and retrieving bounded context with provenance;
- running an interactive or non-interactive coding prompt;
- inspecting architecture, dependency impact, configuration, sessions,
  workflow state, evidence, or runtime capabilities;
- reviewing changes or running explicit verification gates;
- managing RSCH-specific memory, research records, skills, plugins, MCP
  configuration, checkpoints, or background jobs.

Before invoking a command, run `rsch <command> --help`. Use `rsch doctor --json`
to inspect local prerequisites. Prompt and TUI modes require a configured model
provider; browser and external verification capabilities require their own
runtime dependencies.

Do not use RSCH when a deterministic, read-only local command already answers
the question more directly. Do not invoke mutation, removal, commit, pull
request, restore, or unattended-approval paths without the user's authority.

Current limitation: these `agent/` documents are guidance for clients reading
the repository. RSCH does not yet return them inline with every command.
