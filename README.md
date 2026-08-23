# computah

[![CI](https://github.com/davasorus/computah/actions/workflows/ci.yml/badge.svg)](https://github.com/davasorus/computah/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A self-hosted, terminal-based AI coding agent in Go. It runs against a local
OpenAI-compatible model server (e.g. [LM Studio](https://lmstudio.ai/)) — no
cloud, no API keys. It reads and edits code in a working directory, runs
commands with approval, keeps resumable sessions, and extends itself with MCP
tool servers.

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

Values resolve **flag → `COMPUTAH_*` env → config file → auto-detect**.

## Configuration

Two layers, both optional:

- **CLI surface** (`url`, `model`) via Viper: a flag, a `COMPUTAH_URL` /
  `COMPUTAH_MODEL` env var, or `~/.agent/computah.yaml`.
- **Agent behavior** (compaction, budgets, MCP servers, hooks, etc.) via the
  agent's own `~/.agent/config.json`, unchanged.

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
