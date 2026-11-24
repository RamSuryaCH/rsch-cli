---
name: rsch-verification-and-evidence
description: Run RSCH verification and interpret persisted evidence without overstating results.
---

# Verification and evidence

Use native Go checks for source changes, starting narrow and expanding when
appropriate:

```sh
gofmt -w path/to/changed.go
go test ./path/to/changed/package
go test ./...
go vet ./...
```

Use RSCH's evidence layer separately:

```sh
rsch verify --json
rsch verify --ci --json
rsch verify --gate git --json
rsch verify --gate webdriver --json
rsch evidence --json
rsch replay <task-id> --json
rsch recover <task-id> --json
```

`verify --ci` uses stable failure status for incomplete verification.
`verify --gate webdriver` requires a configured WebDriver endpoint and reports
unavailable capability as not ready. `recover` creates an inspection summary;
it does not re-execute recorded actions.

Treat only observed command results as evidence. Record the command, scope,
environment, exit status, and relevant output. A model response, a generated
plan, or the presence of an evidence record is not by itself proof that tests
or acceptance criteria passed.
