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
- **`internal/agent/tools.go`**: tool-approval prompts, rejection/blocked messages, run_command trace lines, streamed shell-output dimming codes. Real candidates for migration, but this is the single largest and most behavior-sensitive file in the package (approval gating) — needs its own dedicated pass with careful manual verification, not a mechanical sweep.
- **`internal/agent/session.go`**: `/sessions`, `/tree`, and `/compact` output.
- **`internal/agent/roles.go`**: `/agents` listing output.
- **`internal/agent/mcp.go`**: MCP server connect/fail status lines (startup-time, before the REPL loop is fully wired).
- **`internal/agent/stats.go`**: `statsRecorder.print()` — thin wrapper already delegating to `render()`; low priority.
- **`internal/agent/loop.go`**: a handful of bare `fmt.Println()` calls that just terminate a streamed line (paired with token-by-token `EmitToken` output) plus one status line.

Recommendation: treat these as **Phase 1b**, scoped and executed file-by-file (tools.go first, given it's the safety-critical approval path), rather than bundled into "Phase 1" as originally scoped.

## Notes

- Use `core.EmitStatus` for general status updates.
- Use `core.EmitError` for failures or errors.
- Use `core.EmitDiff` for pre-formatted, already-styled multi-line output (diffs) that must print verbatim.
- The goal is to decouple logic from presentation entirely — but terminal-control codes and true in-place UI mechanics (spinner, cursor erase) are a separate concern from status/error communication and need Bus support before they can move.
