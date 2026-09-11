// Prompt construction — every model-facing prompt string lives here,
// separate from the execution loop that decides WHEN to send it. Extracted
// from loop.go, session.go, context.go, and checkpoint.go as a pure
// text-building layer: no I/O, no globals mutated, explicit inputs. See
// docs/PHASE4_LOGIC_DECOUPLING.md for the rationale and rollout plan.
//
// buildSystemPrompt (main.go) stays where it is — it was already its own
// function and moving it would be pure churn.
package agent

import (
	"fmt"
	"strings"
)

// ---------- Turn control (loop.go: RunTurn) ----------

// promptProgressCheck is the soft check-in injected at iters == 15 and 28.
// By that point a turn is either deep in legitimate work or wandering — the
// anchor tells the model which, because the original instruction has long
// since scrolled out of attention.
func promptProgressCheck(iters int, anchor string, todos []todoItem) string {
	note := "[Progress check — this turn has used " + fmt.Sprint(iters) + " tool calls. The task is: " + anchor +
		" — If you are advancing it, continue. If you are exploring without progress, state your conclusion or blocker to the user instead of continuing.]"
	if len(todos) > 0 {
		done := 0
		for _, td := range todos {
			if td.Done {
				done++
			}
		}
		note += fmt.Sprintf(" [Checklist: %d/%d done — update it if that's stale.]", done, len(todos))
	}
	return note
}

// promptTurnBudgetReached tells the model its hard tool-call budget is up.
func promptTurnBudgetReached() string {
	return "[Turn budget reached. Stop calling tools. Summarize what you accomplished, what remains, and what you recommend next.]"
}

// promptTurnInterrupted tells the model the user interrupted the turn, so it
// picks up the next instruction directly instead of restarting deliberation.
func promptTurnInterrupted(anchor string) string {
	return "[The user interrupted this turn. The task was: " + anchor +
		" — wait for their next instruction and follow it directly; do not restart deliberation from scratch.]"
}

// ---------- Loop breaker (loop.go: RunTurn, native tool-call path) ----------

// promptIdenticalFailedCallRedirect is stage 1: refuse a call byte-identical
// to the one that just failed, but keep the turn alive with a viable plan B
// — in practice the model usually has one, and cutting the turn there just
// forces the user to type "continue".
func promptIdenticalFailedCallRedirect() string {
	return "[Not executed: this is byte-identical to the call that just failed, so it would fail the same way. " +
		"Do something DIFFERENT: re-read the file and copy its exact bytes into old_str, rewrite the file with write_file, " +
		"or take another approach. Do not submit this call again.]"
}

// promptIdenticalFailedCallStop is stage 2: the model repeated the exact
// same failing call again — stop and hand control back to the user.
func promptIdenticalFailedCallStop() string {
	return "[Not executed: repeated failing call. Stop calling tools. Briefly explain to the user what you were trying to do and what you need from them.]"
}

// promptIdenticalSuccessRefusal refuses an identical read-only call repeated
// in the recent window: it cannot produce new information, and its result
// is already in the conversation.
func promptIdenticalSuccessRefusal() string {
	return "[Not executed: this call is byte-identical to your previous one and nothing has changed — " +
		"its result is already in the conversation above. Use that result, or take a different action.]"
}

// ---------- Loop breaker (loop.go: RunTurn, inline-JSON fallback path) ----------

// promptInlineRepeatedCallStop is the inline-JSON path's loop breaker: a
// model that repeats the exact same call after seeing its error can't
// recover on its own.
func promptInlineRepeatedCallStop() string {
	return "[You repeated the same failed call. Stop calling tools. Explain briefly to the user what you were trying to do and what you need from them.]"
}

// promptInlineToolResult wraps an inline (non-native) tool result as a user
// message, since no real tool_call_id exists to carry it as role=tool.
// multiCall adds a note that any other calls in the same reply were ignored.
func promptInlineToolResult(toolName, result string, multiCall bool) string {
	note := ""
	if multiCall {
		note = "\n\n[Note: your other tool calls were ignored. Make ONE tool call, wait for its result, then continue.]"
	}
	return fmt.Sprintf("[tool result for %s]\n%s%s", toolName, result, note)
}

// ---------- Cap-stall recovery (loop.go: RunTurn) ----------

// promptCapStallNudge recovers from a reasoning model burning its entire
// generation budget deliberating and getting truncated BEFORE its first
// tool call — all thinking, no action, turn over. (Observed: 8192 tokens
// re-litigating a two-function fix, twice in a row, zero edits.) Ending the
// turn there wastes everything the model just worked out; a direct order to
// act usually converts it.
func promptCapStallNudge() string {
	return "[Your reply was truncated at the generation cap and contained no tool call — analysis only. " +
		"Do NOT resume the analysis. State your decision in one sentence, then immediately make the next tool call.]"
}

// ---------- Plan mode (loop.go: runPlanTurn) ----------

// promptPlanModeTask frames a plan-mode turn: explore read-only, then
// produce a plan without modifying anything yet.
func promptPlanModeTask(task string) string {
	return "PLAN MODE (read-only): " + task + "\n\n" +
		"Explore whatever you need with the read-only tools, then produce a concise numbered implementation plan: " +
		"which files change and how, in what order, and how the result gets verified. Do not modify anything yet."
}

// promptPlanApproved tells the model its plan was approved and to execute it.
func promptPlanApproved() string {
	return "Approved. Execute the plan above."
}

// ---------- Verify loop (loop.go: RunVerifyLoop) ----------

// promptVerifyFeedback reports a verify-command failure back to the model:
// parsed failures give it a target (failing tests + file:line assertions, or
// compiler error lines) instead of a wall of output; the raw tail follows as
// backup. cmd is the verify command, detail is the exit/error detail,
// parsedFailures is core.ParseTestFailures's output (empty if none), and
// outputTail is the already-truncated raw output.
func promptVerifyFeedback(cmd, detail, parsedFailures, outputTail string) string {
	feedback := fmt.Sprintf("[verify] `%s` failed (%s).", cmd, detail)
	if parsedFailures != "" {
		feedback += "\n" + parsedFailures + "\nFull output tail:\n" + outputTail
	} else {
		feedback += " Output tail:\n" + outputTail
	}
	feedback += "\n\nFix the underlying problem. Never weaken, skip, or delete tests to make verification pass."
	return feedback
}

// ---------- Subtasks (loop.go: toolSpawnTask) ----------

// promptSubtaskRole frames a delegated subtask: the sub-agent completes only
// its task and returns a summary, since the parent sees only that summary,
// not its tool calls.
func promptSubtaskRole() string {
	return "\n\nYou are handling a DELEGATED SUBTASK from a parent agent. Complete only this task. " +
		"When done, reply with a concise result summary: what you did, files changed, and anything the parent needs to know. " +
		"The parent sees only your final summary, not your tool calls."
}

// ---------- Session (session.go) ----------

// promptTitleSystem is the system prompt for generateTitle's one-line
// session-title request.
func promptTitleSystem() string {
	return "Summarize what this coding session was about in ONE line, at most 8 words, no punctuation at the end, no quotes. Respond with the title only."
}

// promptCompactRequest asks the model to summarize the session for /compact.
func promptCompactRequest() string {
	return "Summarize this session for future context: key decisions, files changed and why, " +
		"and any unresolved threads. Be concise (under 300 words). Output only the summary."
}

// promptCompactCarryOver frames the compacted summary as the start of the
// fresh, post-compact context.
func promptCompactCarryOver(summary string) string {
	return "Context carried over from a previous session (compacted summary):\n" + summary
}

// ---------- Context v2 (context.go: distill) ----------

// promptSessionState builds the v2 wire context's session-state block: the
// ground truth that must never scroll away (modified files, checklist, and
// a note that older tool outputs were elided).
func promptSessionState(modifiedFiles []string, todos []todoItem) string {
	var state strings.Builder
	state.WriteString("[Session state]\n")
	if len(modifiedFiles) > 0 {
		seen := map[string]bool{}
		state.WriteString("Files modified this session: ")
		var uniq []string
		for _, p := range modifiedFiles {
			if !seen[p] {
				seen[p] = true
				uniq = append(uniq, p)
			}
		}
		state.WriteString(strings.Join(uniq, ", ") + "\n")
	}
	if len(todos) > 0 {
		state.WriteString("Checklist:\n")
		for _, td := range todos {
			box := "[ ]"
			if td.Done {
				box = "[x]"
			}
			state.WriteString("  " + box + " " + td.Text + "\n")
		}
	}
	state.WriteString("Earlier tool outputs were elided from this context to keep it small — re-read files or re-run read-only commands when you need their current content.")
	return state.String()
}

// promptSessionGoalPrefix prefixes the session's original goal when it has
// scrolled out of the distilled dialogue window.
const promptSessionGoalPrefix = "[Session goal, from the start of this session] "

// ---------- Checkpoint (checkpoint.go: handleCommit) ----------

// promptCommitSystem is the system prompt for /commit's message generation.
func promptCommitSystem() string {
	return "You write git commit messages following the Conventional Commits standard: " +
		"type(scope): description — types: feat, fix, docs, style, refactor, perf, test, build, ci, chore. " +
		"Imperative mood, subject ≤72 chars, blank line, then a body for non-trivial changes. " +
		"Breaking changes get ! after the type/scope. Respond with the commit message ONLY — no fences, no commentary."
}
