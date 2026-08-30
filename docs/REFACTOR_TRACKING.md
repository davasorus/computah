# Communication Standard Refactor Tracking

Goal: Replace direct terminal output (`fmt.Println`, `fmt.Printf`) with standardized event emissions (`core.EmitStatus`, `core.EmitError`) in the `internal/agent` package.

## Progress Tracker (original tracked scope)

- [x] **`internal/agent/checkpoint.go`**: All `fmt.Print*` output calls replaced with `core.EmitStatus`/`core.EmitError`.
- [x] **`internal/agent/cmdtools.go`**: All `fmt.Printf` status/error lines replaced with `core.EmitStatus`.
- [x] **`internal/agent/eval.go`**: Eval progress, PASS/FAIL, and summary lines standardized on `core.EmitStatus`/`core.EmitError`. Also fixed transcription typos introduced during an earlier pass ("Qewn" -> "Qwen", "-force-ev" -> "-force-eval", "not_later state" -> "not leftover state").
- [x] **`internal/agent/main.go`**: Every `fmt.Println`/`fmt.Printf` call in the REPL loop, slash-command handlers (`/resume`, `/effort`, `/ctx`, `/fork`, `/tools`, `/context`, `/verify`, `/model`, `/help`, `/init`, `/diff`, `/undo`, `/allow`, `/copy`, `/reload`, `/models`), and startup banner converted to `core.EmitStatus`/`core.EmitError`/`core.EmitDiff`.
- [x] **`internal/agent/events_stdout.go`**: Reviewed — this file is intentionally the stdout *rendering* subscriber for the Event Bus (the terminal-facing consumer of `core.Emit*` calls). Its `fmt.Println` calls are the bridge itself, not something to migrate away from.
- [x] **`internal/agent/commands.go`** (added to scope, same pattern): custom-command loader status lines converted to `core.EmitStatus`.

All items above build and pass `go test ./...` / `go vet ./...`.

## Remaining `fmt.Print*` call sites in `internal/agent` (NOT in original tracked scope)

`docs/ROADMAP_PHASE1_COMMUNICATION.md`'s success criterion ("zero fmt.Printf/fmt.Println in internal/agent") is broader than what this tracker covered. The following files still call `fmt.Print*` directly and were intentionally left out of this pass — they need their own review before migrating, because several are not "communication" in the Event Bus sense:

- **`internal/agent/term.go`**: raw ANSI cursor/erase codes (`\r\033[K`), the input prompt (`fmt.Print("> ")`), and the spinner line. These manipulate the terminal cursor in place; forcing them through the Event Bus (which is meant for discrete, one-shot lines a subscriber can render independently) would require Bus support for in-place/overwrite semantics first.
- **`internal/agent/notify.go`**: `fmt.Print("\a")` (terminal bell) — arguably fine to leave as-is; not really an "event" a TUI/Web subscriber renders.
- [x] **`internal/agent/tools.go`** (Phase 1b, done): tool-approval prompts, rejection/blocked messages, run_command trace lines, todo rendering, and `Sandbox.Summary()` migrated to `emitLine`/`emitLineC` (the agent-package aliases for `core.EmitLine`/`core.EmitLineC`). The two `fmt.Print("\033[2m"/"\033[0m")` calls in `ExecShell` are left as-is — they wrap *live-streamed* shell output in a dim ANSI code, which is in-place terminal control, not a discrete line the Bus model represents. Verified with `go build ./...` and `go test ./...`.
- [x] **`internal/agent/session.go`** (Phase 1b, done): mid-turn context-pressure notices, `/sessions`, `/tree`, and `/compact` output migrated to `emitLine`/`emitLineC`.
- [x] **`internal/agent/roles.go`** (Phase 1b, done): `/agents` listing and the startup roles-loaded line migrated to `emitLine`.
- [x] **`internal/agent/mcp.go`** (Phase 1b, done): MCP server connect/fail status lines migrated to `emitLine`.
- [x] **`internal/agent/stats.go`** (Phase 1b, done): `statsRecorder.print()` now calls `emitLine` (trims the trailing newline `render()` had appended for the old `fmt.Print`).
- [x] **`internal/agent/loop.go`** (Phase 1b, done): the `/plan`-declined status line migrated to `emitLine`. The bare `fmt.Println()` calls at lines 179/220/260/345 are intentionally left as-is — they terminate a line whose content was already streamed token-by-token directly to stdout by `md.MDWriter` (outside the Bus, by design; see `events_stdout.go`'s note on `EvToken`/`EvAssistant`), so migrating just the trailing newline would split one logical line across two rendering paths.

All of the above build cleanly (`go build ./...`) and pass `go test ./...`.

## Remaining (deferred by design)

- **`internal/agent/term.go`**: raw ANSI cursor/erase codes (`\r\033[K`), the input prompt (`fmt.Print("> ")`), and the spinner line. These manipulate the terminal cursor in place; forcing them through the Event Bus (which is meant for discrete, one-shot lines a subscriber can render independently) would require Bus support for in-place/overwrite semantics first.
- **`internal/agent/notify.go`**: `fmt.Print("\a")` (terminal bell) — arguably fine to leave as-is; not really an "event" a TUI/Web subscriber renders.

Phase 1b is otherwise complete: every `internal/agent` file with discrete, one-shot status/error output now goes through the Event Bus. What remains (`term.go`, `notify.go`, the two dim-wrap codes in `tools.go`'s `ExecShell`, and the streamed-line newlines in `loop.go`) is in-place terminal control or a direct extension of already-streamed content, not "communication" in the Bus sense — these need Bus support for in-place/overwrite rendering before they can move, or may stay as raw terminal control by design.

## Notes

- Use `core.EmitStatus` for general status updates.
- Use `core.EmitError` for failures or errors.
- Use `core.EmitDiff` for pre-formatted, already-styled multi-line output (diffs) that must print verbatim.
- The goal is to decouple logic from presentation entirely — but terminal-control codes and true in-place UI mechanics (spinner, cursor erase) are a separate concern from status/error communication and need Bus support before they can move.
