package agent

import "testing"

// TestPromptProgressCheck covers both the bare case and the checklist-suffix
// case, since promptProgressCheck's output changes shape based on todos.
func TestPromptProgressCheck(t *testing.T) {
	got := promptProgressCheck(15, "fix the bug", nil)
	want := "[Progress check — this turn has used 15 tool calls. The task is: fix the bug" +
		" — If you are advancing it, continue. If you are exploring without progress, state your conclusion or blocker to the user instead of continuing.]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	withTodos := promptProgressCheck(28, "fix the bug", []todoItem{{Text: "a", Done: true}, {Text: "b"}})
	wantWithTodos := "[Progress check — this turn has used 28 tool calls. The task is: fix the bug" +
		" — If you are advancing it, continue. If you are exploring without progress, state your conclusion or blocker to the user instead of continuing.]" +
		" [Checklist: 1/2 done — update it if that's stale.]"
	if withTodos != wantWithTodos {
		t.Fatalf("got %q, want %q", withTodos, wantWithTodos)
	}
}

func TestPromptTurnBudgetReached(t *testing.T) {
	want := "[Turn budget reached. Stop calling tools. Summarize what you accomplished, what remains, and what you recommend next.]"
	if got := promptTurnBudgetReached(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptTurnInterrupted(t *testing.T) {
	got := promptTurnInterrupted("do the thing")
	want := "[The user interrupted this turn. The task was: do the thing" +
		" — wait for their next instruction and follow it directly; do not restart deliberation from scratch.]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptIdenticalFailedCallRedirect(t *testing.T) {
	want := "[Not executed: this is byte-identical to the call that just failed, so it would fail the same way. " +
		"Do something DIFFERENT: re-read the file and copy its exact bytes into old_str, rewrite the file with write_file, " +
		"or take another approach. Do not submit this call again.]"
	if got := promptIdenticalFailedCallRedirect(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptIdenticalFailedCallStop(t *testing.T) {
	want := "[Not executed: repeated failing call. Stop calling tools. Briefly explain to the user what you were trying to do and what you need from them.]"
	if got := promptIdenticalFailedCallStop(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptIdenticalSuccessRefusal(t *testing.T) {
	want := "[Not executed: this call is byte-identical to your previous one and nothing has changed — " +
		"its result is already in the conversation above. Use that result, or take a different action.]"
	if got := promptIdenticalSuccessRefusal(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptInlineRepeatedCallStop(t *testing.T) {
	want := "[You repeated the same failed call. Stop calling tools. Explain briefly to the user what you were trying to do and what you need from them.]"
	if got := promptInlineRepeatedCallStop(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptInlineToolResult(t *testing.T) {
	single := promptInlineToolResult("read_file", "contents", false)
	if want := "[tool result for read_file]\ncontents"; single != want {
		t.Fatalf("got %q, want %q", single, want)
	}
	multi := promptInlineToolResult("read_file", "contents", true)
	if want := "[tool result for read_file]\ncontents" +
		"\n\n[Note: your other tool calls were ignored. Make ONE tool call, wait for its result, then continue.]"; multi != want {
		t.Fatalf("got %q, want %q", multi, want)
	}
}

func TestPromptCapStallNudge(t *testing.T) {
	want := "[Your reply was truncated at the generation cap and contained no tool call — analysis only. " +
		"Do NOT resume the analysis. State your decision in one sentence, then immediately make the next tool call.]"
	if got := promptCapStallNudge(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptPlanModeTask(t *testing.T) {
	got := promptPlanModeTask("add a health endpoint")
	want := "PLAN MODE (read-only): add a health endpoint\n\n" +
		"Explore whatever you need with the read-only tools, then produce a concise numbered implementation plan: " +
		"which files change and how, in what order, and how the result gets verified. Do not modify anything yet."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptPlanApproved(t *testing.T) {
	if got := promptPlanApproved(); got != "Approved. Execute the plan above." {
		t.Fatalf("got %q", got)
	}
}

func TestPromptVerifyFeedback(t *testing.T) {
	withParsed := promptVerifyFeedback("go test ./...", "exit 1", "FAIL: TestX", "tail...")
	want := "[verify] `go test ./...` failed (exit 1).\nFAIL: TestX\nFull output tail:\ntail...\n\n" +
		"Fix the underlying problem. Never weaken, skip, or delete tests to make verification pass."
	if withParsed != want {
		t.Fatalf("got %q, want %q", withParsed, want)
	}

	noParsed := promptVerifyFeedback("go build ./...", "exit 2", "", "compile error tail")
	want2 := "[verify] `go build ./...` failed (exit 2). Output tail:\ncompile error tail\n\n" +
		"Fix the underlying problem. Never weaken, skip, or delete tests to make verification pass."
	if noParsed != want2 {
		t.Fatalf("got %q, want %q", noParsed, want2)
	}
}

func TestPromptSubtaskRole(t *testing.T) {
	want := "\n\nYou are handling a DELEGATED SUBTASK from a parent agent. Complete only this task. " +
		"When done, reply with a concise result summary: what you did, files changed, and anything the parent needs to know. " +
		"The parent sees only your final summary, not your tool calls."
	if got := promptSubtaskRole(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptTitleSystem(t *testing.T) {
	want := "Summarize what this coding session was about in ONE line, at most 8 words, no punctuation at the end, no quotes. Respond with the title only."
	if got := promptTitleSystem(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptCompactRequest(t *testing.T) {
	want := "Summarize this session for future context: key decisions, files changed and why, " +
		"and any unresolved threads. Be concise (under 300 words). Output only the summary."
	if got := promptCompactRequest(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptCompactCarryOver(t *testing.T) {
	got := promptCompactCarryOver("summary text")
	want := "Context carried over from a previous session (compacted summary):\nsummary text"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptSessionState(t *testing.T) {
	got := promptSessionState([]string{"a.go", "a.go", "b.go"}, []todoItem{{Text: "x", Done: true}, {Text: "y"}})
	want := "[Session state]\n" +
		"Files modified this session: a.go, b.go\n" +
		"Checklist:\n" +
		"  [x] x\n" +
		"  [ ] y\n" +
		"Earlier tool outputs were elided from this context to keep it small — re-read files or re-run read-only commands when you need their current content."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptSessionStateEmpty(t *testing.T) {
	got := promptSessionState(nil, nil)
	want := "[Session state]\n" +
		"Earlier tool outputs were elided from this context to keep it small — re-read files or re-run read-only commands when you need their current content."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPromptCommitSystem(t *testing.T) {
	want := "You write git commit messages following the Conventional Commits standard: " +
		"type(scope): description — types: feat, fix, docs, style, refactor, perf, test, build, ci, chore. " +
		"Imperative mood, subject ≤72 chars, blank line, then a body for non-trivial changes. " +
		"Breaking changes get ! after the type/scope. Respond with the commit message ONLY — no fences, no commentary."
	if got := promptCommitSystem(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
