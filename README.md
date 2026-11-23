# Stead

**I’ll go instead.**

Stead is the volunteer tucked inside *instead*. Hand it a task, and that part of your day is yours again.

[Project docs](https://app.notion.com/p/sanduo/Stead-ed7c56617fb883acb3c2016d8f9d07ff) — Stead’s core idea and product design.

## Local development

Use Go 1.27.0 or later. Run these commands from the repository root.

```sh
go run ./cmd/stead --help
go build -o ./stead ./cmd/stead
./stead --help
```

The CLI currently displays help with `--help`, `-h`, or no arguments.

To verify the project, build and test all packages.

```sh
go build ./...
go test ./...
```

## Project layout

`cmd/stead/` contains the CLI entry point. The following packages are planned in the [kernel design](https://app.notion.com/p/421c56617fb881768a5bf017788881bd).

- `internal/delegation/` holds the delegation model, state transitions, runtime loop, and plugin interfaces.
- `internal/ledger/` holds the MySQL InnoDB ledger and its GORM persistence code.
- `internal/scenarios/scheduling/` holds the scheduling decision plugin.
- `internal/channels/feishu/` holds the adapter for incoming Feishu events and outgoing actions.
- `internal/simulation/` holds the controllable clock, fake executor, and fault injection tools.
