---
name: rsch-getting-started
description: Build, configure, diagnose, and run RSCH for the first time.
---

# Getting started

The source checkout requires Go 1.26 or newer.

```sh
go build ./...
go test ./...
go run . --version
go run . doctor --json
```

Configure a supported provider with its documented environment variable, such
as `OPENAI_API_KEY`, rather than placing a key on the command line. If multiple
providers are configured, choose the coder model explicitly in `rsch.json`.
Validate configuration before starting model work:

```sh
go run . config validate
go run . config hierarchy --json
```

Run the TUI only from an interactive terminal:

```sh
go run .
```

For automation, provide a prompt explicitly or through bounded stdin and opt
into JSON output:

```sh
go run . --prompt "Explain the request path" --output-format json --no-input
printf '%s\n' "Explain the request path" | go run . --output-format json
```

Prompt mode may execute tools, but non-interactive approvals are denied by
default. `--auto-approve` and its `--yes` alias authorize unattended permission
requests and must only be used after explicit user approval.

This guide is not automatically installed into the runtime `rsch skill`
catalog. Use `rsch skill --help` to inspect that separate feature.
