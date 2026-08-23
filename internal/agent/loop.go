package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/davasorus/computah/internal/core"
	"github.com/davasorus/computah/internal/md"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// parseInlineToolCalls extracts tool calls a model emitted as JSON in its text
// (in ```json fences or raw) instead of the structured tool_calls field.
// Dormant with models that emit native calls (Gemma does); kept as insurance
// for any future model whose chat template lacks tool support.
func parseInlineToolCalls(content string) []ToolCall {
	var calls []ToolCall
	for _, chunk := range strings.Split(content, "```") {
		chunk = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(chunk), "json"))
		if !strings.HasPrefix(chunk, "{") {
			continue
		}
		var raw struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(chunk), &raw); err != nil || raw.Name == "" {
			continue
		}
		args, _ := json.Marshal(raw.Arguments)
		var tc ToolCall
		tc.ID = fmt.Sprintf("inline_%d", len(calls))
		tc.Type = "function"
		tc.Function.Name = raw.Name
		tc.Function.Arguments = string(args)
		calls = append(calls, tc)
	}
	return calls
}

// ---------- Agent loop ----------

// interrupt receives SIGINT (Ctrl+C). During a turn it aborts the in-flight
// request; at the prompt it does nothing (Ctrl+D exits).
var interrupt = make(chan os.Signal, 1)

// interruptibleChat runs chat() with a context that Ctrl+C cancels. Returns
// (message, interrupted, err).
func interruptibleChat(baseURL, model string, messages []Message, onToken func(string)) (Message, bool, error) {
	// Drain any stale Ctrl+C pressed at the prompt or during a command —
	// otherwise it would instantly cancel this fresh request.
	select {
	case <-interrupt:
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-interrupt:
			cancel() // aborts the HTTP stream mid-generation
		case <-done:
		}
	}()
	reply, err := chat(ctx, baseURL, model, messages, onToken)
	if ctx.Err() == context.Canceled {
		// Keep whatever streamed: the model's partial reply is often its
		// CONCLUSION — interrupts land right when the user has seen enough,
		// which is typically the moment the model finally decided.
		return reply, true, nil
	}
	return reply, false, err
}

// streamChat wraps interruptibleChat with the spinner: an animated status
// line runs until the first token (or the tool calls) arrive, then vanishes.
func streamChat(baseURL, model string, messages []Message) (Message, bool, error) {
	const maxAttempts = 2
	var reply Message
	var intr bool
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		streamed := false
		mdw := md.NewMDWriter() // markdown → ANSI, line-buffered (see md.go)
		sp := startSpinner(thinkingLabels[rand.Intn(len(thinkingLabels))])
		// Surface tool-call generation on the spinner: without this, a long
		// (or runaway) call is silent — no tokens print, nothing moves.
		onToolProgress = func(name string, chars int) {
			sp.SetLabel(fmt.Sprintf("writing %s call (~%d tokens)", name, chars/4))
		}
		onReasoning = func(chars int) {
			sp.SetLabel(fmt.Sprintf("thinking (~%d tokens)", chars/4))
			emitThinking(fmt.Sprintf("thinking (~%d tokens)", chars/4))
		}
		reply, intr, err = interruptibleChat(baseURL, model, messages, func(tok string) {
			streamed = true
			sp.Stop()
			mdw.Write(tok)
		})
		onToolProgress, onReasoning = nil, nil
		sp.Stop()
		mdw.Flush()
		// Retry only when: it failed, wasn't a user interrupt, and nothing
		// was printed yet (retrying after partial output would duplicate it).
		if err != nil && !intr && !streamed && attempt < maxAttempts {
			emitLineC(cDim, fmt.Sprintf("  (request failed: %v — retrying once)", err))
			time.Sleep(time.Second)
			continue
		}
		break
	}
	return reply, intr, err
}

// runTurn is the agent loop: send the conversation, stream the reply,
// execute any tool calls (read-only ones concurrently), append results, and
// repeat until the model answers with plain text. No iteration cap by
// design — the loop breaker and the user's Ctrl+C are the exits.
func RunTurn(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message) []Message {
	emitBusy(true)
	defer emitBusy(false)
	lastCall := ""      // signature of the previous single tool call, to break retry loops
	lastErr := false    // whether that call's result was an error
	breakerFires := 0   // per-turn count of refused repeats (stage 1 redirects, stage 2 stops)
	capNudges := 0      // per-turn count of act-now nudges after cap-truncated deliberation
	repeatRefusals := 0 // per-turn count of refused identical successful read-only calls
	iters := 0          // tool-loop iterations this turn (soft check-in, hard budget)
	var recentSigs []string
	// The task anchor: what this turn is FOR. Small models drift when the
	// instruction is twenty tool-results back; the anchor is re-injected at
	// the soft check-in so the turn's purpose can't scroll out of mind.
	anchor := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			anchor = messages[i].Content
			if len(anchor) > 300 {
				anchor = anchor[:300] + "…"
			}
			break
		}
	}
	for {
		iters++
		// Soft check-in: by iteration 15 a turn is either deep in legitimate
		// work or wandering — the anchor tells the model which, because the
		// original instruction has long since scrolled out of attention.
		if iters == 15 || iters == 28 {
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
			messages = append(messages, Message{Role: "user", Content: note})
			emitLineC(cDim, fmt.Sprintf("  (progress check injected at %d calls)", iters))
		}
		// Hard budget: past this, the turn ends with an accounting rather
		// than wandering forever. Config "max_turn_iters"; generous default
		// because legitimate big refactors are long — this is a circuit
		// breaker, not a leash.
		if iters > maxTurnIters {
			emitLineC(cRed, fmt.Sprintf("  (turn budget: %d tool calls — stopping)", maxTurnIters))
			messages = append(messages, Message{Role: "user", Content: "[Turn budget reached. Stop calling tools. Summarize what you accomplished, what remains, and what you recommend next.]"})
			if final, intr, err := streamChat(baseURL, model, messages); err == nil && !intr {
				messages = append(messages, final)
				fmt.Println()
			}
			return messages
		}
		// Persist before any shrinking below: the session file must always
		// hold full-fidelity content, and a crash mid-turn then loses at
		// most the in-flight request, not the tool results that led to it.
		st.Append(messages)
		// Mid-turn context safety: one exploratory turn (tree + searches +
		// big command outputs) can blow past the budget between user turns,
		// where auto-compact never gets a chance to run. manageContext is a
		// no-op below the budget (see its KV-cache note).
		messages = manageContext(messages)
		reply, interrupted, err := streamChat(baseURL, model, messages)
		if interrupted {
			// Half-assembled tool calls are dangerous and always dropped —
			// but partial CONTENT is kept: the reply the user interrupted is
			// often the model's just-reached conclusion, and discarding it
			// forced the user to re-explain intent after every Ctrl+C. With
			// it preserved (plus the task anchor in the marker), a bare
			// "continue" or a one-line correction resumes from where the
			// model actually was.
			fmt.Println("\n(turn interrupted — back to you)")
			if strings.TrimSpace(reply.Content) != "" {
				reply.ToolCalls = nil
				reply.Content += "\n[reply cut off here by the user's interrupt]"
				messages = append(messages, reply)
			}
			messages = append(messages, Message{
				Role: "user",
				Content: "[The user interrupted this turn. The task was: " + anchor +
					" — wait for their next instruction and follow it directly; do not restart deliberation from scratch.]",
			})
			return messages
		}
		if err != nil {
			fmt.Println("error:", err)
			return messages
		}
		messages = append(messages, reply)
		if reply.Content != "" {
			fmt.Println() // finish the streamed line(s)
		}

		// Native structured tool calls: execute all (results are keyed by id).
		if len(reply.ToolCalls) > 0 {
			// Loop breaker (native path), two-stage. A model re-issuing the
			// exact same single call after seeing it fail can't recover by
			// repetition — and with no iteration cap, that's an infinite
			// loop. Stage 1: refuse the call but keep the turn ALIVE with
			// corrective guidance — in practice the model usually has a
			// viable plan B (rewrite the file) and cutting the turn there
			// just forces the user to type "continue". Stage 2 (it repeats
			// AGAIN): refuse, let it explain once, hand control back.
			if len(reply.ToolCalls) == 1 {
				sig := reply.ToolCalls[0].Function.Name + reply.ToolCalls[0].Function.Arguments
				recentSigs = append(recentSigs, sig)
				if len(recentSigs) > 8 {
					recentSigs = recentSigs[len(recentSigs)-8:]
				}
				if sig == lastCall && lastErr {
					breakerFires++
					if breakerFires == 1 {
						emitLineC(cYellow, "  (refused: identical to the call that just failed — redirecting)")
						messages = append(messages, Message{
							Role:       "tool",
							ToolCallID: reply.ToolCalls[0].ID,
							Content: "[Not executed: this is byte-identical to the call that just failed, so it would fail the same way. " +
								"Do something DIFFERENT: re-read the file and copy its exact bytes into old_str, rewrite the file with write_file, " +
								"or take another approach. Do not submit this call again.]",
						})
						continue
					}
					emitLineC(cRed, "  (stopped: model repeated the same failed tool call again)")
					messages = append(messages, Message{
						Role:       "tool",
						ToolCallID: reply.ToolCalls[0].ID,
						Content:    "[Not executed: repeated failing call. Stop calling tools. Briefly explain to the user what you were trying to do and what you need from them.]",
					})
					if final, intr, err := streamChat(baseURL, model, messages); err == nil && !intr {
						messages = append(messages, final)
						fmt.Println()
					}
					return messages
				}
				// Success-repeat, WINDOWED: an identical read-only call
				// repeated anywhere in the recent window cannot produce new
				// information — and the observed loops aren't back-to-back,
				// they're the same search with two other calls between
				// (A-B-A-B cycles, the grep issued four times across a
				// turn). Third occurrence in the window gets refused. Only
				// side-effect-free calls: repeating a mutating command can
				// be deliberate.
				if sigCount(recentSigs[:len(recentSigs)-1], sig) >= 2 && repeatRefusals < 2 && isIdempotentCall(reply.ToolCalls[0]) {
					repeatRefusals++
					emitLineC(cYellow, "  (refused: identical to the previous call — its result is already above)")
					messages = append(messages, Message{
						Role:       "tool",
						ToolCallID: reply.ToolCalls[0].ID,
						Content: "[Not executed: this call is byte-identical to your previous one and nothing has changed — " +
							"its result is already in the conversation above. Use that result, or take a different action.]",
					})
					continue
				}
			}
			// Read-only tools are side-effect-free and can run concurrently
			// when the model batches them (common during exploration). Tools
			// that write, run commands, or prompt the user must stay
			// sequential and in order. Results are always appended in the
			// model's original call order, so tool_call_ids line up.
			results := make([]string, len(reply.ToolCalls))
			var wg sync.WaitGroup
			for i, call := range reply.ToolCalls {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
					args = map[string]any{}
				}
				emitToolCall(call.Function.Name, preview(call.Function.Arguments))
				if readOnlyTools[call.Function.Name] {
					wg.Add(1)
					go func(idx int, name string, a map[string]any) {
						defer wg.Done()
						results[idx] = sb.Execute(name, a)
					}(i, call.Function.Name, args)
				} else {
					// Wait for any in-flight parallel reads to finish before a
					// side-effecting call, so ordering of effects is preserved.
					wg.Wait()
					results[i] = sb.Execute(call.Function.Name, args)
				}
			}
			wg.Wait()
			for i, call := range reply.ToolCalls {
				messages = append(messages, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    results[i],
				})
			}
			if len(reply.ToolCalls) == 1 {
				lastCall = reply.ToolCalls[0].Function.Name + reply.ToolCalls[0].Function.Arguments
				lastErr = strings.HasPrefix(results[0], "ERROR")
			} else {
				lastCall, lastErr = "", false // a batch is fresh progress
			}
			continue
		}

		// Fallback: tool calls emitted as JSON in the text. Execute ONLY the
		// first one — later calls in the same response were planned before
		// seeing results, so their arguments are speculative.
		if inline := parseInlineToolCalls(reply.Content); len(inline) > 0 {
			call := inline[0]

			// Loop breaker: a model that repeats the exact same call after
			// seeing its error can't recover on its own. Stop, tell it to
			// explain itself, and hand control back to the user.
			sig := call.Function.Name + call.Function.Arguments
			if sig == lastCall {
				fmt.Print("\n(stopped: model repeated the same failed tool call)\n")
				messages = append(messages, Message{
					Role:    "user",
					Content: "[You repeated the same failed call. Stop calling tools. Explain briefly to the user what you were trying to do and what you need from them.]",
				})
				if final, intr, err := streamChat(baseURL, model, messages); err == nil && !intr {
					messages = append(messages, final)
					fmt.Println()
				}
				return messages
			}
			lastCall = sig

			var args map[string]any
			if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
				args = map[string]any{}
			}
			emitToolCallInline(call.Function.Name, preview(call.Function.Arguments))
			result := sb.Execute(call.Function.Name, args)
			lastErr = strings.HasPrefix(result, "ERROR")

			note := ""
			if len(inline) > 1 {
				note = "\n\n[Note: your other tool calls were ignored. Make ONE tool call, wait for its result, then continue.]"
			}
			// No real tool_call_id exists on the assistant message, so feed
			// the result back as a user message; a role=tool message without
			// a matching id can be rejected or mangled by the template.
			messages = append(messages, Message{
				Role:    "user",
				Content: fmt.Sprintf("[tool result for %s]\n%s%s", call.Function.Name, result, note),
			})
			continue
		}

		// Cap-stall recovery: a reasoning model can burn the entire
		// generation budget deliberating and get truncated BEFORE its first
		// tool call — all thinking, no action, turn over. (Observed: 8192
		// tokens re-litigating a two-function fix, twice in a row, zero
		// edits.) Ending the turn there wastes everything the model just
		// worked out; a direct order to act usually converts it. Bounded at
		// two nudges — past that the model is stuck and the user should see.
		if lastFinishReason == "length" && capNudges < 2 {
			capNudges++
			emitLineC(cYellow, "  (cap reached mid-deliberation with no action — nudging the model to act)")
			messages = append(messages, Message{
				Role: "user",
				Content: "[Your reply was truncated at the generation cap and contained no tool call — analysis only. " +
					"Do NOT resume the analysis. State your decision in one sentence, then immediately make the next tool call.]",
			})
			continue
		}

		// Plain text (already streamed live): end of turn.
		return messages
	}
}

// maxTurnIters is the hard per-turn tool-call budget (config "max_turn_iters").
var maxTurnIters = 40

// sigCount counts occurrences of sig in the window.
func sigCount(window []string, sig string) int {
	n := 0
	for _, s := range window {
		if s == sig {
			n++
		}
	}
	return n
}

// isIdempotentCall reports whether repeating this exact call cannot change
// anything: read-only tools always, and run_command only when the command is
// on the auto-approved (read-only) allowlist. A repeated mutating command —
// rerunning a flaky test, retrying a migration — can be intentional and is
// never refused.
func isIdempotentCall(tc ToolCall) bool {
	if readOnlyTools[tc.Function.Name] {
		return true
	}
	if tc.Function.Name != "run_command" {
		return false
	}
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(tc.Function.Arguments), &a) != nil {
		return false
	}
	return builtinAutoApproved(strings.TrimSpace(a.Command))
}

// readOnlyTools are side-effect-free and safe to run concurrently when the
// model batches them in one turn. Everything else (write_file, edit_file,
// run_command, fetch_url, spawn_task, MCP tools) can mutate state or prompt
// the user, so it runs sequentially in call order.
var readOnlyTools = map[string]bool{
	"read_file":    true,
	"list_dir":     true,
	"search_files": true,
	"tree":         true,
	"glob":         true,
}

// ---------- Plan mode ----------

// planMode restricts a turn to read-only tools. For a 12B this matters more
// than for frontier models: reviewing a plan is cheaper than reviewing
// flailed edits. Enforced at BOTH ends — the request only advertises
// read-only tools (currentTools), and Execute refuses anything else anyway
// (belt and suspenders; a model can hallucinate a tool it wasn't offered).
var planMode bool

// currentTools returns the tool schemas for the next request, honoring plan
// mode. Used by chat() instead of the raw global.
func currentTools() []map[string]any {
	if !planMode {
		return tools
	}
	var ro []map[string]any
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		if fn == nil {
			continue
		}
		if name, _ := fn["name"].(string); readOnlyTools[name] {
			ro = append(ro, t)
		}
	}
	return ro
}

// runPlanTurn explores read-only, produces a plan, and on approval executes
// it as a normal turn (verify loop applies via the caller's flow).
func runPlanTurn(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message, task string) []Message {
	planMode = true
	messages = append(messages, Message{
		Role: "user",
		Content: "PLAN MODE (read-only): " + task + "\n\n" +
			"Explore whatever you need with the read-only tools, then produce a concise numbered implementation plan: " +
			"which files change and how, in what order, and how the result gets verified. Do not modify anything yet.",
	})
	messages = runTurn(baseURL, model, sb, st, messages)
	planMode = false
	st.Append(messages)
	ans, ok := askLine("execute this plan? [y/N/edit] ")
	if !ok {
		return messages
	}
	switch strings.ToLower(ans) {
	case "y", "yes":
		messages = append(messages, Message{Role: "user", Content: "Approved. Execute the plan above."})
		return runTurn(baseURL, model, sb, st, messages)
	case "edit":
		note, nok := askLine("what should change about the plan? ")
		if !nok || strings.TrimSpace(note) == "" {
			return messages
		}
		return runPlanTurn(baseURL, model, sb, st, messages, "Revise the previous plan: "+note)
	default:
		fmt.Println("(plan kept in context, nothing executed — refine it or /plan again)")
		return messages
	}
}

// ---------- Verify loop ----------

// runVerifyLoop closes the loop against ground truth: when a turn modified
// files and a verify command is configured, the HARNESS (not the model) runs
// it, and failures are fed back as a new turn — up to maxFixAttempts times.
// This converts "a model that edits files" into "a system that converges on
// working code": the model's claim of being done is checked, every time.
// If a fix attempt modifies nothing, retrying is pointless and control
// returns to the user immediately.
func RunVerifyLoop(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message, modifiedBefore int) []Message {
	const maxFixAttempts = 2
	if verifyCommand == "" || len(sb.Modified) == modifiedBefore {
		return messages
	}
	for attempt := 0; ; attempt++ {
		emitLineC(cDim, "  $ "+verifyCommand+" (verify)")
		out, code, err := execShell(verifyCommand, sb.Root, true)
		if err == nil && code == 0 {
			emitLineC(cGreen, "  ✓ verify passed")
			return messages
		}
		detail := fmt.Sprintf("exit %d", code)
		if err != nil {
			detail = err.Error()
		}
		emitLineC(cRed, "  ✗ verify failed ("+detail+")")
		if attempt >= maxFixAttempts {
			emitLineC(cYellow, "  (verify still failing after fix attempts — over to you)")
			return messages
		}
		// Parsed failures give the model a target (failing tests +
		// file:line assertions, or the compiler error lines) instead of a
		// wall of output; the raw tail follows as backup.
		feedback := fmt.Sprintf("[verify] `%s` failed (%s).", verifyCommand, detail)
		if parsed := core.ParseTestFailures(out); parsed != "" {
			feedback += "\n" + parsed + "\nFull output tail:\n" + tail(out, 2048)
		} else {
			feedback += " Output tail:\n" + tail(out, 4096)
		}
		feedback += "\n\nFix the underlying problem. Never weaken, skip, or delete tests to make verification pass."
		messages = append(messages, Message{Role: "user", Content: feedback})
		before := len(sb.Modified)
		messages = runTurn(baseURL, model, sb, st, messages)
		st.Append(messages)
		if len(sb.Modified) == before {
			emitLineC(cYellow, "  (fix attempt changed no files — stopping verify retries)")
			return messages
		}
	}
}

// ---------- Subtasks ----------

// spawnDepth guards against subtasks spawning subtasks: one level is
// decomposition, two is the model disappearing up its own recursion.
var spawnDepth int

// toolSpawnTask runs a self-contained subtask in a FRESH context: a new
// message list with the same system prompt and tools, sharing the Sandbox
// (so backups, Modified tracking, and guards still apply). Only the final
// summary returns to the parent. This is the real fix for context growth on
// big tasks — exploration garbage from step 1 never pollutes step 8 — and
// it's why elision/compaction are palliatives once this exists.
func toolSpawnTask(s *Sandbox, a toolArgs) string {
	task := strings.TrimSpace(a.str("task"))
	if task == "" {
		return "ERROR: task must describe the subtask to perform"
	}
	if spawnDepth >= 1 {
		return "ERROR: subtasks cannot spawn further subtasks — do this work directly"
	}
	spawnDepth++
	defer func() { spawnDepth-- }()

	subModel := curModel
	rolePrompt := "\n\nYou are handling a DELEGATED SUBTASK from a parent agent. Complete only this task. " +
		"When done, reply with a concise result summary: what you did, files changed, and anything the parent needs to know. " +
		"The parent sees only your final summary, not your tool calls."
	label := "subtask"
	if roleName := strings.TrimSpace(a.str("role")); roleName != "" {
		role, ok := agentRoles[roleName]
		if !ok {
			return fmt.Sprintf("ERROR: no agent role %q — available: %s. Omit role to run a plain subtask.", roleName, roleNames())
		}
		rolePrompt += "\n\nYour role for this subtask:\n" + role.Prompt
		if role.Model != "" {
			subModel = role.Model
		}
		label = "subtask [" + roleName + "]"
	}
	emitLineC(cCyan, "  ⧉ "+label+": "+preview(task))
	sub := []Message{
		{Role: "system", Content: buildSystemPrompt(s.Root) + rolePrompt},
		{Role: "user", Content: task},
	}
	// Subtask internals aren't persisted (disabled store) — the parent
	// transcript records the spawn call and the returned summary, and file
	// changes are visible in Modified/.bak as usual.
	sub = runTurn(curBaseURL, subModel, s, &SessionStore{}, sub)
	emitLineC(cCyan, "  ⧉ "+label+" finished")
	for i := len(sub) - 1; i >= 0; i-- {
		if sub[i].Role == "assistant" && strings.TrimSpace(sub[i].Content) != "" {
			return sub[i].Content
		}
	}
	return "(subtask completed but produced no summary — check the trace above for what it did)"
}

func roleNames() string {
	if len(agentRoles) == 0 {
		return "(none defined)"
	}
	var names []string
	for n := range agentRoles {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// preview truncates long tool arguments (e.g. full file contents) so trace
