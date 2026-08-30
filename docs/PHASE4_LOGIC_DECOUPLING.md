# Phase 4 — Logic Decoupling: Implementation Plan

## Source

This implements Phase 4 of the `computah` audit roadmap (stored in the
memory MCP as `computah-audit-roadmap`):

> **Phase 4: Logic Decoupling** — Move prompt construction logic to a
> dedicated layer, separate from the execution loop in `loop.go`.

Phases 1, 1b, 2, and 3 (communication standardization, error handling,
configuration extraction) are DONE. This phase is unstarted.

## Current state vs. the roadmap note

The roadmap note is accurate: prompt text is built inline, as string
literals and `fmt.Sprintf`/concatenation, at the exact point each prompt is
needed — mixed in with the control flow that decides *when* to send it. A
full codebase sweep found this is not confined to `loop.go`; the same
pattern repeats in three other files that each independently talk to a
model:

| File | Function | Prompt(s) built inline |
|---|---|---|
| `loop.go` | `RunTurn` | progress-check note (2 trip points + checklist suffix), turn-budget-reached instruction, interrupted-turn continuation note |
| `loop.go` | `RunTurn` (loop breaker, native path) | identical-failed-call redirect, identical-failed-call stop, identical-successful-call refusal |
| `loop.go` | `RunTurn` (loop breaker, inline-JSON path) | repeated-failed-call stop, inline tool-result wrapper + multi-call note |
| `loop.go` | `RunTurn` (cap-stall recovery) | act-now nudge after truncated deliberation |
| `loop.go` | `runPlanTurn` | plan-mode task framing, plan-approved instruction |
| `loop.go` | `RunVerifyLoop` | verify-failure feedback (command, exit detail, parsed failures, output tail, "never weaken tests" instruction) |
| `loop.go` | `toolSpawnTask` | subtask role-framing preamble (+ role's own `Prompt` appended) |
| `session.go` | `generateTitle` | title-summarization system prompt |
| `session.go` | `Compact` | compaction-request instruction, carried-over-summary framing |
| `context.go` | `distill` | session-state block (modified files, checklist, elision note), session-goal-preserved framing |
| `checkpoint.go` | `handleCommit` | commit-message system prompt |
| `main.go` | `buildSystemPrompt` | the main system prompt (already isolated in its own function — the one place this pattern was already followed) |

`buildSystemPrompt` is the existing proof that this works: `eval.go`,
`loop.go` (subtask spawn), and `main.go` all call it instead of building the
prompt three times. Phase 4 generalizes that one success to every other
prompt in the codebase.

## Why this matters (not just tidiness)

- **Untestable in isolation today.** Asserting the exact wording of the
  progress-check note, the verify-feedback message, or the loop-breaker
  refusal requires driving the full `RunTurn` state machine (fake HTTP
  server, multi-iteration setup) just to inspect a string. A pure prompt
  function is a one-line table test.
- **Duplication risk.** The loop-breaker's "identical failed call" framing
  appears twice with near-identical wording (native path and inline-JSON
  path) already drifting slightly — a future edit to one is unlikely to be
  mirrored in the other because they don't look related in the diff.
- **`RunTurn` is doing two jobs.** It is ~230 lines mixing turn-control
  state (iteration counters, breaker counts, signature tracking) with
  prompt authorship (what exact words tell the model to stop). Splitting
  the latter out makes the control flow in `RunTurn` itself readable: each
  site becomes `messages = append(messages, Message{Role: "user", Content:
  promptXxx(...)})` instead of a multi-line literal breaking the flow.

## What is explicitly OUT of scope

- **`buildSystemPrompt` itself stays in `main.go`.** It is already its own
  function with a clear name and doc comment; moving it purely for file
  organization is churn without benefit. It's referenced from the new file
  via the normal same-package call (no import needed — see below).
- **No new Go package.** Every candidate function depends on
  package-private context (`todos`, `verifyCommand`, `agentRoles`,
  `Message`, `ToolCall`) that lives in `package agent`. Moving prompt
  construction into a separate importable package (e.g.
  `internal/agent/prompts`) would force either (a) exporting a dozen
  internal types/globals solely so the new package can read them, which
  makes the *coupling* worse, not better, or (b) threading all of that
  through function parameters, which is a much larger, riskier refactor
  than "extract the string-building." A new **file**,
  `internal/agent/prompts.go`, in the same package delivers the roadmap's
  actual goal — prompt text lives in one place, separate from the
  execution loop — without that cost. Full package extraction is left as a
  possible future phase if the prompt layer grows enough to justify it.
- **Emitted status/log lines are not prompts.** `emitLineC(cYellow, "
  (refused: ...)")` etc. are user-facing trace output (already migrated to
  the Event Bus in Phase 1/1b), not text sent to the model. Only
  `Message{...Content: ...}` construction destined for a `chat()` call is
  in scope here.
- **No behavior or wording changes.** This is a pure extract-function
  refactor: every prompt's exact text, including existing minor
  inconsistencies (e.g. the two slightly different loop-breaker phrasings),
  moves verbatim. Reconciling that duplication is a natural follow-up once
  it's visible side-by-side in one file, but is not bundled into this
  mechanical move so the diff stays reviewable and risk-free.

## Proposed layer: `internal/agent/prompts.go`

One new file, `package agent`, holding every model-facing prompt-text
constructor as a small pure function: explicit parameters in, a `string`
out, no I/O, no globals read where the value can instead be passed in.
Where a function's only "state" is genuinely global and static (e.g. the
commit-message system prompt has no parameters at all), it's a plain
`const`/function with no arguments — no need to invent parameters nobody
will vary.

Grouped by call site, in call order:

```go
package agent

import "fmt"

// ---------- Turn control (loop.go: RunTurn) ----------

// promptProgressCheck is the soft check-in injected at iters == 15 and 28.
func promptProgressCheck(iters int, anchor string, todos []Todo) string

// promptTurnBudgetReached tells the model its hard tool-call budget is up.
func promptTurnBudgetReached() string

// promptTurnInterrupted tells the model the user interrupted the turn.
func promptTurnInterrupted(anchor string) string

// ---------- Loop breaker (loop.go: RunTurn, native tool-call path) ----------

func promptIdenticalFailedCallRedirect() string
func promptIdenticalFailedCallStop() string
func promptIdenticalSuccessRefusal() string

// ---------- Loop breaker (loop.go: RunTurn, inline-JSON fallback path) ----------

func promptInlineRepeatedCallStop() string
func promptInlineToolResult(toolName, result string, multiCall bool) string

// ---------- Cap-stall recovery (loop.go: RunTurn) ----------

func promptCapStallNudge() string

// ---------- Plan mode (loop.go: runPlanTurn) ----------

func promptPlanModeTask(task string) string
func promptPlanApproved() string

// ---------- Verify loop (loop.go: RunVerifyLoop) ----------

func promptVerifyFeedback(cmd, failDetail, parsedFailures, outputTail string) string

// ---------- Subtasks (loop.go: toolSpawnTask) ----------

func promptSubtaskRole() string

// ---------- Session (session.go) ----------

func promptTitleSystem() string
func promptCompactRequest() string
func promptCompactCarryOver(summary string) string

// ---------- Context v2 (context.go: distill) ----------

func promptSessionState(modifiedFiles []string, todos []Todo) string
const promptSessionGoalPrefix = "[Session goal, from the start of this session] "

// ---------- Checkpoint (checkpoint.go: handleCommit) ----------

func promptCommitSystem() string
```

Each function's doc comment carries over the *reasoning* comment that sits
next to today's inline literal (e.g. `promptCapStallNudge`'s comment
explains the "8192 tokens re-litigating a two-function fix" observation
that justifies the nudge) — that context is valuable and must not be lost
in the move, just relocated with the text it explains.

## File-by-file changes

### New: `internal/agent/prompts.go`
Add all functions above, each populated with today's exact literal text
(byte-for-byte), doc comments carried over from the call sites.

### `internal/agent/loop.go`
Replace each inline `Content: "..."` / `Content: fmt.Sprintf(...)` block at
the sites in the table above with a call to the matching `promptXxx(...)`.
`RunTurn`, `runPlanTurn`, `RunVerifyLoop`, and `toolSpawnTask` keep all
their control-flow logic (counters, breaker state, retry loop) — only the
text construction moves.

### `internal/agent/session.go`
`generateTitle` and `Compact` call `promptTitleSystem()`,
`promptCompactRequest()`, `promptCompactCarryOver(reply.Content)`.

### `internal/agent/context.go`
`distill` calls `promptSessionState(uniqModifiedFiles, todos)` for the
state block, and uses `promptSessionGoalPrefix` instead of the inline
string literal.

### `internal/agent/checkpoint.go`
`handleCommit` calls `promptCommitSystem()`.

### `internal/agent/main.go`
No change. `buildSystemPrompt` stays as-is (see "out of scope" above).

## Test plan

New `internal/agent/prompts_test.go`:
- One test per non-trivial function asserting exact output for representative
  inputs (e.g. `promptProgressCheck` with and without a non-empty todo
  list; `promptVerifyFeedback` with and without parsed failures;
  `promptSubtaskRole` is parameterless and gets a single exact-match test).
- A regression test asserting `RunTurn`'s progress-check injection still
  fires at iteration 15/28 and its *content* matches
  `promptProgressCheck(...)` (keeps the call-site wiring covered, not just
  the function in isolation).
- Existing tests that assert on message content by substring (e.g. any
  test checking `strings.Contains(msg.Content, "...")` for loop-breaker or
  verify-feedback text) must keep passing unmodified — this is the
  regression signal that no wording moved or changed during extraction.

## Verification

After implementation: `go build ./...`, `go vet ./...`, `go test ./...`.
Since this is a pure refactor (no behavior change), a `git diff` review
should show only "moved code," and the full existing test suite is the
correctness net — no test should need its expected strings changed.

## Rollout order (avoid breaking the build mid-change)

1. Create `prompts.go` with all functions, populated from the current
   inline text (compiles standalone; nothing calls it yet — dead code is
   fine transiently within one commit).
2. Update `loop.go` call sites, one function (`RunTurn`, then
   `runPlanTurn`, then `RunVerifyLoop`, then `toolSpawnTask`) at a time,
   compiling after each.
3. Update `session.go` (`generateTitle`, `Compact`).
4. Update `context.go` (`distill`).
5. Update `checkpoint.go` (`handleCommit`).
6. Add `prompts_test.go`.
7. Run full verification.

Steps 2–5 are independent of each other and could be done in any order or
combined; the sequence above is simply smallest-diff-first for easy review.

## Explicitly deferred

- Reconciling the two slightly different loop-breaker phrasings (native
  vs. inline-JSON path) into one shared wording — visible only once both
  live side-by-side in `prompts.go`; a natural fast-follow, not bundled
  here to keep this change behavior-neutral.
- Extracting `buildSystemPrompt` into `prompts.go` — left in `main.go`,
  see "out of scope."
- Splitting prompt text into external template files (e.g. `.txt`/`.tmpl`
  assets) for non-Go-recompile edits — not requested by the roadmap, and
  Go string constants are simpler to keep in sync with the code paths that
  reason about them (e.g. `promptVerifyFeedback` referencing
  `core.ParseTestFailures`'s output shape).
- A real `internal/agent/prompts` package — see "no new Go package" above;
  revisit only if the prompt layer's size or reuse needs cross the
  threshold where the parameter-threading cost becomes worth it.
