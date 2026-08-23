// Headless mode — the agent driven solely from the web dashboard.
//
// No terminal reader, no TUI. The loop blocks on browser submissions and
// runs each as a turn, with all output flowing through the event bus to the
// dashboard's SSE stream. This is the intended shape for "run it on a box,
// interact from a browser (phone, laptop) on the LAN": there's no human at
// the terminal, so there's nothing to select against — the browser queue is
// the only input source, which makes this the simplest and most robust of
// the three front-end paths.
//
// Input parity with the REPL: a submitted line is classified the same way
// (!shell / @file / /command / prompt) so the dashboard user gets the same
// affordances. Read-only /commands run via the shared dispatcher; stateful
// ones report that they need the REPL (a headless session can't, e.g.,
// /reload itself).
package agent

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func runHeadless(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message) {
	// Headless has no terminal, so tool approvals must go to the browser.
	setApprovalMode(approvalWeb)
	// The dashboard renders bus events; make sure stdout also logs a little
	// so the operator watching the process sees life. We keep the stdout
	// subscriber active (not silenced) so `docker logs` / the terminal shows
	// activity, but there's no interactive prompt.
	emitStatus("headless: no terminal UI — drive from the dashboard at the served address")
	fmt.Println("headless mode: open the dashboard to interact. Ctrl+C to quit.")

	// Clean shutdown on SIGINT/SIGTERM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		emitStatus("headless: shutting down")
		// best-effort session flush already happens per-turn
		os.Exit(0)
	}()

	for {
		text, ok := <-browserSubmissions
		if !ok {
			return
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		emitUser(text)

		kind, arg := classifyInput(text)
		switch kind {
		case inputBlank:
			continue

		case inputShell:
			out, code, err := execShell(arg, sb.Root, false)
			detail := ""
			if err != nil {
				detail = " " + err.Error()
			}
			emitLine(tail(out, 4096))
			messages = append(messages, Message{
				Role:    "user",
				Content: "[shell] $ " + arg + " (exit " + strconv.Itoa(code) + detail + ")\n" + tail(out, 8192),
			})
			st.Append(messages)

		case inputFile:
			data, err := os.ReadFile(arg)
			if err != nil {
				emitError("  @ cannot read " + arg + ": " + err.Error())
				continue
			}
			messages = append(messages, Message{
				Role:    "user",
				Content: "[attached " + arg + "]\n" + string(data),
			})
			st.Append(messages)
			emitLine("  @ attached " + arg + " (" + strconv.Itoa(len(data)) + " bytes)")

		case inputCommand:
			if out, handled := runInfoCommand(arg, baseURL, model, messages, st); handled {
				emitLine(strings.TrimRight(out, "\n"))
			} else {
				emitStatus("  " + arg + " — stateful commands aren't available in headless mode (no REPL)")
			}

		case inputPrompt:
			messages = append(messages, Message{Role: "user", Content: text})
			messages = runTurn(baseURL, model, sb, st, messages)
			messages = runVerifyLoopIfConfigured(baseURL, model, sb, st, messages)
			st.Append(messages)
		}
	}
}

// runVerifyLoopIfConfigured runs the verify loop when a verify command is
// set, mirroring what the REPL does after a normal turn. Kept small and
// separate so headless stays readable.
func runVerifyLoopIfConfigured(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message) []Message {
	if verifyCommand == "" {
		return messages
	}
	// modifiedBefore=0: the verify loop re-checks build/tests regardless.
	return runVerifyLoop(baseURL, model, sb, st, messages, 0)
}
