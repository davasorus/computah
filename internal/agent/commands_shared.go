// Shared slash-command dispatch for the read-only / informational commands.
//
// The REPL historically handled every slash command inline in its input
// loop, printing directly. That's fine for the REPL but leaves the TUI
// unable to run any command (it has no access to that loop). This file
// extracts the SAFE subset — commands that only READ and report state — into
// runInfoCommand, which returns its output as a string instead of printing.
// The REPL prints the string; the TUI emits it to the bus. One source of
// truth, and the TUI gains real slash-command support for everything that
// doesn't mutate session state.
//
// STATEFUL commands (/plan, /commit, /rewind, /compact, /fork, /reload,
// /model, /init, /undo, /allow, /ctx, /resume, /diff, /verify) are NOT here:
// they mutate messages/session/tree and are entangled with the REPL loop.
// Routing those through a shared dispatcher safely is a larger refactor;
// they remain REPL-only, and the TUI reports as much for them.
package agent

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// capture runs f with os.Stdout redirected to a pipe and returns everything
// it printed. This lets the shared dispatcher reuse the REPL's existing
// printer functions (listServerModels, printSessionTree, …) verbatim instead
// of rewriting each into a string builder — thorough coverage, minimal risk.
// Serialized: concurrent captures would fight over os.Stdout.
var captureMu sync.Mutex

func capture(f func()) string {
	captureMu.Lock()
	defer captureMu.Unlock()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		f() // fall back to direct output on pipe failure
		return ""
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	f()
	w.Close()
	os.Stdout = orig
	return <-done
}

// infoCommands is the set runInfoCommand handles — the read-only ones.
var infoCommands = map[string]bool{
	"/help": true, "/tools": true, "/stats": true, "/budget": true,
	"/context": true, "/models": true, "/tree": true, "/sessions": true,
	"/todos": true, "/effort": true, "/agents": true,
}

// isInfoCommand reports whether cmd (first word) is a read-only command.
func isInfoCommand(cmd string) bool {
	return infoCommands[strings.Fields(strings.TrimSpace(cmd))[0]]
}

// runInfoCommand executes a read-only command and returns (output, handled).
// handled is false if the command isn't a known read-only one (the caller
// should then treat it as a stateful/REPL command). It needs a bit of
// context (model, messages, session) to render some reports.
func runInfoCommand(cmd, baseURL, model string, messages []Message, st *SessionStore) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) == 0 {
		return "", false
	}
	switch fields[0] {
	case "/help":
		return helpText(), true
	case "/tools":
		var b strings.Builder
		b.WriteString("available tools:\n")
		for _, t := range registry {
			fmt.Fprintf(&b, "  %-24s %s\n", t.Name, firstSentence(t.Desc))
		}
		return b.String(), true
	case "/stats":
		return stats.render(), true
	case "/budget":
		return renderBudget(), true
	case "/context":
		return fmt.Sprintf("context: ~%d tokens of a %d-token budget (%d messages)\n",
			estimateTokens(messages), autoCompactTokens, len(messages)), true
	case "/models":
		return capture(func() { listServerModels(baseURL, model) }), true
	case "/tree":
		return capture(func() { st.printSessionTree() }), true
	case "/sessions":
		return capture(func() { st.listSessions() }), true
	case "/todos":
		return capture(renderTodos), true
	case "/effort":
		if len(fields) > 1 {
			return "", false // /effort <level> mutates state — REPL handles it
		}
		return renderEffort(), true
	case "/agents":
		return capture(func() { listAgentRoles() }), true
	}
	return "", false
}

func helpText() string {
	return "commands: /plan <task> /init /verify /commit /rewind /stats /budget /models /effort /reload /fork /tree /ctx /compact /context /diff [path] /undo <path> /resume /sessions /tools /model [id] /allow [prefix] /copy /todos /agents /help\n" +
		"input: Tab completes /commands and paths · ↑/↓ history · !cmd runs shell · @path attaches a file · \"\"\" opens multi-line\n"
}

// renderEffort reports the current reasoning-effort configuration.
func renderEffort() string {
	normal := reasoningEffort
	if normal == "" {
		normal = "(model default)"
	}
	plan := planReasoningEffort
	if plan == "" {
		plan = "(same as normal)"
	}
	return fmt.Sprintf("reasoning effort: %s · plan mode: %s\n", normal, plan)
}
