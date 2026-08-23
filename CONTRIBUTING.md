# Contributing to computah

computah is a personal self-hosted AI coding agent, but issues and PRs are welcome.

## Development

Requires Go 1.25+.

```bash
git clone https://github.com/davasorus/computah
cd computah
make build      # or: go build -o computah .
make test       # go test -race ./...
make lint       # golangci-lint run ./...
```

computah talks to a local OpenAI-compatible model server (e.g. LM Studio). Point
it at yours with `computah run --url http://localhost:1234/v1 --model your-model`.

## Architecture

The code is split into focused packages under `internal/`:

- **core** — shared types (Message, Event), the event bus, styling, and config primitives. Imports nothing.
- **agent** — the engine: sandbox, tool registry, model loop, sessions, approvals, MCP.
- **md** — markdown rendering for the terminal.
- **tui** — the full-screen Bubble Tea interface.
- **web** — the read-only / two-way / headless web dashboard.
- **tools_ext** — engine tool plug-ins (decision, structured edits, embeddings, vault).
- **cmd** — the Cobra command tree; wires the UI and tool packages into the engine via hooks.

Presentation and tool packages depend on `agent`; `agent` never imports them —
the wiring is inverted through hooks in `cmd`, so there are no import cycles.

## Before submitting

- `make test` and `make lint` must pass.
- Keep commits conventional (`feat:`, `fix:`, `docs:`, `build:`, `ci:`) — the
  release changelog is generated from them.
