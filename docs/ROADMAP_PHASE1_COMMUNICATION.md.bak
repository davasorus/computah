# Phase 1: Communication Standard & Event Bus Migration

## Overview

The goal of Phase 1 is to move away from direct terminal output (`fmt.Println`, `fmt.Printf`) and transition toward a decoupled, event-driven communication model. This ensures that the core logic remains independent of the presentation layer (TUI, Web Dashboard, or CLI).

## The Event Bus Architecture

The **Event Bus** (`internal/core/events.go`) is the "shared output spine" of the application. 

### Core Concepts:

- **Decoupling**: Instead of writing to stdout, logic components *emit* typed events.
- **Subscribers**: Different interfaces (TUI, Web, CLI) subscribe to these events and render them according to their own requirements.
- **Backward Compatibility**: During migration, existing `fmt` calls remain but are supplemented by the Bus. The "stdout" subscriber ensures that output remains consistent for terminal users while new features can be hooked into the bus immediately.

## Event Types (`EventKind`)

| Kind | Usage | Replacement For |
| :--- | :--- | :--- |
| `EvLine` | General status/trace lines | `fmt.Println` / `fmt.Printf` |
| `EvToken` | Streamed assistant content tokens | LLM stream output |
| `EvThinking` | Reasoning progress/counts | Internal reasoning states |
| `EvToolCall` | Tool invocation (name + args) | Logic-specific tool logging |
| `EvToolDone` | Tool result summaries | Post-tool execution logs |
| `EvUser` | User input messages | Incoming message handling |
| `EvAssistant` | Completed assistant messages | Outgoing response completion |
| `EvStats` | Budget/status snapshots | Progress bars and counters |
| `EvStatus` | Connection/mode updates | System-level state changes |
| `EvError` | Error reporting | `fmt.Errorf` or error logging |
| `EvBusy` | Turn lifecycle (1=working, 0=idle) | Loading spinners / UI indicators |
| `EvApproval` | User interaction required | Web/TUI modal triggers |

## Migration Strategy: `internal/agent`

The following files in `internal/agent` were refactored to replace raw `fmt` calls with the appropriate `core.EmitX` functions.

### Target Files & Tasks: STATUS

- [x] **`internal/agent/checkpoint.go`**: Internal state check logs and progress markers now use `core.EmitStatus`/`core.EmitError`.
- [x] **`internal/agent/cmdtools.go`**: Tool interaction feedback converted to `core.EmitStatus` (manifest errors, shadow warnings, load summary).
- [x] **`internal/agent/eval.go`**: Evaluation results standardized on `core.EmitStatus`/`core.EmitError`.
- [x] **`internal/agent/main.go`**: The REPL loop, all slash-command handlers, and the startup banner migrated to the event model.
- [x] **`internal/agent/commands.go`**: Custom-command loader status lines migrated (added to scope alongside main.go).
- [x] **`internal/agent/events_stdout.go`**: Confirmed as the intended stdout-rendering bridge — its `fmt.Println` calls are the terminal subscriber's own rendering, not migration targets.

See `docs/REFACTOR_TRACKING.md` for the detailed file-by-file record.

### Not yet migrated (Phase 1b — separate, scoped effort)

Achieving literal "zero `fmt.Printf`/`fmt.Println` in `internal/agent`" also requires touching `tools.go` (tool-approval prompts — safety-critical, needs its own careful pass), `session.go`, `roles.go`, `mcp.go`, `stats.go`, `term.go` (raw ANSI cursor control — needs Bus support for in-place updates before it can move), `notify.go`, and a few lines in `loop.go`. These were deliberately left out of this pass; see `docs/REFACTOR_TRACKING.md` for the reasoning per file.

## Success Criteria

1. Zero instances of `fmt.Printf` or `fmt.Println` remaining in `internal/agent`'s core command/status paths (checkpoint, cmdtools, eval, main, commands) — **met**.
2. All core agent logic in those files communicates exclusively via `core.Emit*` functions — **met**.
3. The TUI and Web Dashboards can consume the same event stream from the Bus without modification to the underlying agent logic — **met for the migrated files**; unaffected by the Phase 1b remainder since those paths already worked before this change.

Full-package zero-`fmt` (including `tools.go`, `term.go`, etc.) is deferred to Phase 1b, tracked separately in `docs/REFACTOR_TRACKING.md`.
