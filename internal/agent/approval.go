// Approval broker — routes tool-approval prompts to whatever interface can
// actually answer them.
//
// Mutating commands and tools prompt for approval ([y/N/a]). Historically
// that prompt was a terminal read (askLine), which works for the REPL but
// hangs headless (no terminal) and can't reach the web UI. The broker
// abstracts "ask the user to approve X" so it can be answered from the
// terminal OR the browser, depending on which front-end is driving.
//
//   - Terminal mode (default, plain REPL): prompts via askLine as before.
//   - Web mode (headless, or -serve-write when chosen): emits an EvApproval
//     event the dashboard renders as Approve/Deny/Always buttons, and blocks
//     on a response delivered via POST /api/approve.
package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// approvalDecision is the normalized answer regardless of source.
type approvalDecision int

const (
	approveOnce   approvalDecision = iota // run this once
	approveAlways                         // run and add to allowlist
	approveDeny                           // refuse
)

// approvalMode selects how approvals are gathered.
type approvalModeT int

const (
	approvalTerminal approvalModeT = iota // askLine on the terminal
	approvalWeb                           // route to the dashboard
)

type approvalBrokerT struct {
	mu   sync.Mutex
	mode approvalModeT
	// pending is the in-flight web approval (only one at a time — tool
	// execution is serial per turn). Answer is delivered on the channel.
	pending *pendingApproval
}

type pendingApproval struct {
	id     string
	prompt string
	tool   string
	respCh chan approvalDecision
}

var approvals = &approvalBrokerT{}

// setApprovalMode is called at startup once the front-end is known.
func SetApprovalMode(m approvalModeT) {
	approvals.mu.Lock()
	approvals.mode = m
	approvals.mu.Unlock()
}

// request asks the user to approve an action. tok is the command/tool token
// used for the "always allow" label. Returns the decision.
func (b *approvalBrokerT) request(prompt, tok string) approvalDecision {
	b.mu.Lock()
	mode := b.mode
	b.mu.Unlock()

	if mode == approvalTerminal {
		return b.requestTerminal(prompt, tok)
	}
	return b.requestWeb(prompt, tok)
}

func (b *approvalBrokerT) requestTerminal(prompt, tok string) approvalDecision {
	answerLine, ok := askLine(prompt)
	if !ok {
		return approveDeny
	}
	switch normalizeApproval(answerLine) {
	case "y":
		return approveOnce
	case "a":
		return approveAlways
	default:
		return approveDeny
	}
}

func (b *approvalBrokerT) requestWeb(prompt, tok string) approvalDecision {
	p := &pendingApproval{
		id:     fmt.Sprintf("apr-%d", time.Now().UnixNano()),
		prompt: prompt,
		tool:   tok,
		respCh: make(chan approvalDecision, 1),
	}
	b.mu.Lock()
	b.pending = p
	b.mu.Unlock()

	// Tell the dashboard to show approve/deny buttons.
	bus.Emit(Event{Kind: EvApproval, Text: prompt, Tool: tok, Meta: map[string]string{"id": p.id}})

	// Block until the browser answers (or a long timeout as a safety net so
	// a closed browser can't wedge the agent forever).
	select {
	case d := <-p.respCh:
		b.clearPending(p.id)
		return d
	case <-time.After(10 * time.Minute):
		b.clearPending(p.id)
		emitError("  approval timed out after 10 min with no response — denying")
		return approveDeny
	}
}

func (b *approvalBrokerT) clearPending(id string) {
	b.mu.Lock()
	if b.pending != nil && b.pending.id == id {
		b.pending = nil
	}
	b.mu.Unlock()
}

// answerWeb delivers a browser decision to the waiting request. Returns false
// if there's no matching pending approval (stale/duplicate click).
func (b *approvalBrokerT) answerWeb(id string, d approvalDecision) bool {
	b.mu.Lock()
	p := b.pending
	b.mu.Unlock()
	if p == nil || p.id != id {
		return false
	}
	select {
	case p.respCh <- d:
		return true
	default:
		return false
	}
}

// normalizeApproval maps a raw text answer to y/a/n.
func normalizeApproval(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return "y"
	case "a", "always":
		return "a"
	default:
		return "n"
	}
}
