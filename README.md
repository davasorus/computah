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

## AI Usage

- This was created using a combination of Online Claude Code and offline [gemma-4-12B](https://huggingface.co/google/gemma-4-12B)

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
  agent's own `~/.agent/config.json`, unchanged. Set `price_in_per_m` /
  `price_out_per_m` (USD per 1M tokens) to see an estimated session cost in
  `/stats` when using a paid endpoint; local servers leave them unset. Set
  `plan_model` to route `/plan` turns to a stronger model while normal
  execution stays on the main (faster) model. Set `fast_model` to route
  trivial follow-up turns (e.g. "continue", "commit that", "run the tests") to
  a cheaper/faster model automatically; substantive turns stay on the main
  model.

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
