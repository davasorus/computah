// /stats — throughput made visible.
//
// You're tuning VRAM, speculative decoding, and KV-cache behavior by feel;
// this turns feel into numbers. Everything here is measured at the harness
// (token counts are the same chars/4 estimate the context budget uses, so
// they're consistent with /context), because local servers are inconsistent
// about reporting usage on streamed responses.
//
// The number that matters most on local hardware: TTFB (time to first
// byte of generation) ≈ prompt processing time. Append-only turns keep it
// small via KV-cache prefix reuse; anything that edited history (shrink,
// prune, compact, resume) shows up here as a spike. When /stats says the
// last request spent 90 seconds before the first token, that was the
// cache-invalidation bill.
package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type statsRecorder struct {
	mu       sync.Mutex
	requests int
	promptTk int           // cumulative estimated tokens sent
	genTk    int           // cumulative estimated tokens generated
	thinkTk  int           // cumulative reasoning ("thinking") tokens
	genDur   time.Duration // cumulative time spent generating (after first byte)
	ttfbDur  time.Duration // cumulative time to first byte (≈ prompt processing)
	// last completed request
	lastPrompt int
	lastGen    int
	lastThink  int
	lastTTFB   time.Duration
	lastGenDur time.Duration
}

var stats statsRecorder

// priceInPerM / priceOutPerM are USD per 1,000,000 tokens for input (prompt)
// and output (generated + reasoning), set from config at startup. Both zero
// for local/free servers, which suppresses the cost line entirely — so the
// default local experience is unchanged. Output pricing covers reasoning
// tokens too, since providers bill those at the output rate.
var (
	priceInPerM  float64
	priceOutPerM float64
)

// pricingEnabled reports whether a cost estimate can be shown.
func pricingEnabled() bool { return priceInPerM > 0 || priceOutPerM > 0 }

// sessionCostUSD estimates the cumulative session cost from token counts and
// the configured per-million rates. Reasoning tokens bill at the output rate.
func (s *statsRecorder) sessionCostUSD() float64 {
	in := float64(s.promptTk) / 1e6 * priceInPerM
	out := float64(s.genTk+s.thinkTk) / 1e6 * priceOutPerM
	return in + out
}

// record is called once per completed chat request from the SSE layer.
func (s *statsRecorder) record(promptTok, genTok, thinkTok int, ttfb, total time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	genDur := total - ttfb
	if genDur < 0 {
		genDur = 0
	}
	s.requests++
	s.promptTk += promptTok
	s.genTk += genTok
	s.thinkTk += thinkTok
	s.genDur += genDur
	s.ttfbDur += ttfb
	s.lastPrompt, s.lastGen, s.lastThink, s.lastTTFB, s.lastGenDur = promptTok, genTok, thinkTok, ttfb, genDur
}

func (s *statsRecorder) print() { fmt.Print(s.render()) }

// render builds the stats report as a string (testable; used by /stats in
// both the REPL and the TUI).
func (s *statsRecorder) render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests == 0 {
		return "no requests yet this session\n"
	}
	rate := func(tok int, d time.Duration) string {
		if d <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f tok/s", float64(tok)/d.Seconds())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "session: %d request(s) · ~%dk sent · %d thought · %d generated (%s incl. thinking) · avg TTFB %s\n",
		s.requests, s.promptTk/1000, s.thinkTk, s.genTk, rate(s.genTk+s.thinkTk, s.genDur),
		(s.ttfbDur / time.Duration(s.requests)).Round(100*time.Millisecond))
	fmt.Fprintf(&b, "last request: ~%dk sent · %d thought · %d generated · TTFB %s\n",
		s.lastPrompt/1000, s.lastThink, s.lastGen,
		s.lastTTFB.Round(100*time.Millisecond))
	if pricingEnabled() {
		fmt.Fprintf(&b, "estimated cost: $%.4f this session (in $%.2f/M · out $%.2f/M; reasoning billed as output)\n",
			s.sessionCostUSD(), priceInPerM, priceOutPerM)
	}
	b.WriteString("(tokens are chars/4 estimates; TTFB ≈ prompt processing; \"thought\" = reasoning tokens the model burned before answering — the main cost of a reasoning model)\n")
	return b.String()
}
