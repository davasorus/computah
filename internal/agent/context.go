// Context construction v2 — relevance over recency, opt-in.
//
// v1 (the default) sends the whole transcript, append-only, and trims with
// pruning/shrinking/compaction. Its virtue is KV-cache prefix reuse: within
// and across turns the server reprocesses only new suffixes (~25s measured
// for a cold 13k prompt, near-zero warm). Its vice is accumulation: a long
// session drags every stale tool output behind it forever.
//
// v2 REBUILDS the wire context at each turn boundary from what matters now:
//
//	[system prompt]
//	[session state: modified files, checklist, elision note]
//	[the session's original goal, if it scrolled out of the window]
//	[recent dialogue: user messages + final assistant replies only]
//	[the new user instruction]
//
// Old tool outputs are deliberately absent — the model re-reads what it
// needs, and reads are cheap. The trade is a full prompt reprocess once per
// turn (bounded, small prompt) versus v1's growing-but-cached prompt. Which
// wins depends on session length and hardware; that's why this is a live
// toggle (/ctx v2, /ctx v1) and the per-turn cost prints dim — measure,
// then decide.
//
// INVARIANTS the distiller guarantees: never emits tool-role messages or
// assistant messages carrying tool_calls (so the wire can't contain orphaned
// halves of a call/result pair), always includes the newest user message,
// and never touches the canonical transcript — sessions persist full
// fidelity regardless of mode. One honest cost: mid-turn crash persistence
// is weaker in v2 (the turn's messages land in the session file at turn end,
// not per-iteration).
package agent

import (
	"strings"
)

// contextV2 toggles distilled wire context (config "context_v2", /ctx).
var contextV2 bool

// distillBudget caps the recent-dialogue window's estimated tokens.
const distillBudget = 6000

// distill builds the v2 wire context from the canonical transcript.
// messages[0] must be the system prompt; the last message must be the new
// user instruction.
func distill(messages []Message, sb *Sandbox) []Message {
	if len(messages) < 2 {
		return messages
	}
	wire := []Message{messages[0]}

	// Session state block: the ground truth that must never scroll away.
	var state strings.Builder
	state.WriteString("[Session state]\n")
	if len(sb.Modified) > 0 {
		seen := map[string]bool{}
		state.WriteString("Files modified this session: ")
		var uniq []string
		for _, p := range sb.Modified {
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
	wire = append(wire, Message{Role: "user", Content: state.String()})

	// Dialogue window: user messages and FINAL assistant replies only.
	// No tool results, no tool-calling assistant messages — by construction
	// the wire cannot contain half of a call/result pair.
	var dialogue []Message
	for _, m := range messages[1:] {
		switch {
		case m.Role == "user" && !strings.HasPrefix(m.Content, "["):
			dialogue = append(dialogue, m)
		case m.Role == "assistant" && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "":
			dialogue = append(dialogue, m)
		}
	}
	if len(dialogue) == 0 {
		// Degenerate (shouldn't happen: the new user msg qualifies) — fall
		// back to v1 semantics rather than send a task-free context.
		return messages
	}
	// The session's original goal survives even when the window slides past
	// it — small models drift without it (same reasoning as the turn anchor).
	goal := dialogue[0]
	window := dialogue
	for len(window) > 12 || (len(window) > 1 && estimateTokens(window) > distillBudget) {
		window = window[1:]
	}
	if window[0].Content != goal.Content {
		wire = append(wire, Message{Role: "user", Content: "[Session goal, from the start of this session] " + clip(goal.Content, 400)})
	}
	return append(wire, window...)
}
