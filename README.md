# computah

[![CI](https://github.com/davasorus/computah/actions/workflows/ci.yml/badge.svg)](https://github.com/davasorus/computah/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A self-hosted, terminal-based AI coding agent in Go. It runs against any
OpenAI-compatible model server — a local one like [LM Studio](https://lmstudio.ai/)
or [Ollama](https://ollama.com/) with no key required, or an authenticated
cloud/gateway endpoint via an API key. It reads and edits code in a working
directory, runs commands with approval, keeps resumable sessions, and extends
itself with MCP tool servers.

## Install

```bash
go install github.com/davasorus/computah@latest
```

This drops a `computah` binary on your `PATH` (`$(go env GOPATH)/bin`).

## Usage

```bash
computah                    # start the interactive agent in the current dir
computah run ./project      # ...or in a specific working directory
computah run --resume latest
computah exec "add a health endpoint" --yes    # one-shot, non-interactive
computah eval cases.json --runs 3               # run an eval file
computah dashboard --write                      # web dashboard (two-way)
computah dashboard --headless                   # web-only, no terminal UI
computah version
```

`computah` with no subcommand is equivalent to `computah run`.

### Global flags

| Flag | Meaning |
|------|---------|
| `--url` | Base URL of the OpenAI-compatible server (default: auto-detect / config) |
| `--model` | Model id (default: first chat model on the server / config) |

An optional API key for authenticated endpoints (cloud OpenAI, a proxied
gateway) is read from `api_key` in the config file or the `COMPUTAH_API_KEY`
env var; it's sent as a `Bearer` token. Local servers need no key — leave it
unset.

Values resolve **flag → `COMPUTAH_*` env → config file → auto-detect**.

## Configuration

Two layers, both optional:

- **CLI surface** (`url`, `model`) via Viper: a flag, a `COMPUTAH_URL` /
  `COMPUTAH_MODEL` env var, or `~/.agent/computah.yaml`.
- **Agent behavior** (compaction, budgets, MCP servers, hooks, etc.) via the
  agent's own `~/.agent/config.json`.

Copy [`config.example.json`](config.example.json) to `~/.agent/config.json`
and keep only the fields you need — every field is optional and unknown keys
(like the `"// ..."` comments in the example) are ignored, so defaults apply
for anything you omit.

### `~/.agent/config.json` fields

| Field | Default | Purpose |
|-------|---------|---------|
| `url`, `model`, `api_key` | auto / — | server URL, model id, Bearer token for authenticated endpoints |
| `aux_model` | main model | smaller model for compaction, titles, commit messages |
| `plan_model` | main model | stronger model for `/plan` turns |
| `fast_model` | main model | cheaper model for trivial follow-ups ("continue", "commit that") |
| `reasoning_effort` / `plan_reasoning_effort` | — | `low\|medium\|high` thinking budget for normal / `/plan` turns |
| `price_in_per_m` / `price_out_per_m` | 0 (off) | USD per 1M tokens — enables session cost in `/stats` |
| `compact_tokens` | model-based | context size at which history is compacted |
| `max_tokens` | 8192 | per-generation output cap |
| `max_turn_iters` | 40 | hard per-turn tool-call budget |
| `command_timeout_sec` | 300 | `run_command` time limit |
| `budget_minutes` / `budget_ktokens` | 0 (off) | warn past a wall-clock / token budget |
| `protected` | built-in list | extra write-protected globs (e.g. `.env`, `secrets/*`) |
| `verify_command` | — | command the agent can run to self-check (e.g. `go build ./... && go test ./...`) |
| `no_checkpoints` | false | disable per-turn git snapshots |
| `journal` / `audit` | false | write a session summary / structured audit note on exit |
| `vault_path` / `embed_model` | — | Obsidian vault root (+ embedding model) → `vault_search/read/note` |
| `notify_sec` | 10 | toast+bell for turns longer than this (0 = off) |
| `hooks` | — | shell commands at lifecycle points (see below) |
| `mcp_servers` | — | external tool servers (see below) |

### Hooks

`hooks` maps a lifecycle point to a shell command. `{file}` and `{cmd}` are
substituted; a **nonzero `pre_command` exit blocks the command**.

```json
"hooks": {
  "post_edit":   "gofmt -w {file}",
  "pre_command": "true",
  "post_turn":   "git status -sb"
}
```

### MCP servers

`mcp_servers` extends the agent with external [MCP](https://modelcontextprotocol.io/)
tool servers over **stdio** (a child process) or **HTTP**. Set `prefer: true`
to steer the model toward a server in the system prompt, and `prefer_hint` to
describe *how* it should use a non-notes server.

```json
"mcp_servers": {
  "sandbox": {
    "command": "sandbox",
    "args": ["mcp", "-image", "python:3-alpine"],
    "prefer": true,
    "prefer_hint": "Run untrusted or experimental code here, in an isolated container, rather than run_command."
  },
  "remote": {
    "url": "https://mcp.example.com/sse",
    "token": "your-token",
    "headers": { "X-Extra": "value" }
  }
}
```

Per-server keys: `command`/`args`/`env` (stdio) or `url`/`token`/`headers`/`insecure`
(HTTP); `no_prefix` registers tools under their own names; `prefer` / `prefer_hint`
control system-prompt steering.

## Layout

```
computah/
  main.go              entry — hands off to cmd/
  cmd/                 Cobra command tree (run, exec, eval, dashboard, version)
  internal/
    core/              shared types (Message, Event), event bus, styling, config
    agent/             the engine: sandbox, tool registry, loop, sessions, MCP
    md/                terminal markdown rendering
    tui/               full-screen Bubble Tea interface
    web/               read-only / two-way / headless web dashboard
    tools_ext/         engine tool plug-ins (decision, edits, embeddings, vault)
```

`core` imports nothing; `agent` builds on `core`; the presentation packages
(`md`, `tui`, `web`) and `tools_ext` build on `agent`. `agent` never imports
them back — `cmd` injects the UI and tool registrations through hooks, so the
dependency graph stays acyclic.

## Requirements

- Go 1.25+
- A local OpenAI-compatible server (LM Studio, etc.) reachable at `--url`
