// Session budgets — a gentle ceiling on time and tokens.
//
// The per-turn iteration cap stops ONE runaway turn. This stops the other
// failure mode: twenty reasonable turns that quietly add up to ninety
// minutes and a novel's worth of generated tokens before you notice. On a
// local box the cost isn't dollars, it's your afternoon and your GPU — so
// the budget is a WARNING, never a hard stop. Aborting a session mid-thought
// to enforce a ceiling would be worse than the overrun.
//
// Config: "budget_minutes" and/or "budget_ktokens" (thousands of generated
// + reasoning tokens — reasoning counts because on a reasoning model it's
// most of the spend). Zero/unset = that dimension is off. Crossing a
// threshold prints one warning; every further 50% prints a reminder. /budget
// shows current usage against the ceilings anytime.
package agent

import (
	"fmt"
	"strings"
	"time"
)

var (
	budgetMinutes int       // config "budget_minutes" (0 = off)
	budgetKTokens int       // config "budget_ktokens" (0 = off)
	sessionStart  time.Time // set once at REPL entry
	timeWarnedAt  int       // highest minute-threshold already warned (0 = none)
	tokWarnedAt   int       // highest ktoken-threshold already warned
)

// budgetTokensUsed is generated + reasoning tokens — the part that actually
// costs wall-clock on a local model. Prompt tokens are mostly cached reuse.
func budgetTokensUsed() int {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	return stats.genTk + stats.thinkTk
}

// checkBudget is called after each completed turn. It warns on first crossing
// and re-warns each additional 50% over, so a long session keeps a quiet
// drumbeat rather than one easily-missed line.
func checkBudget() {
	if !sessionStart.IsZero() && budgetMinutes > 0 {
		mins := int(time.Since(sessionStart).Minutes())
		// Thresholds at 100%, 150%, 200%, ... of the ceiling.
		step := budgetMinutes / 2
		if step < 1 {
			step = 1
		}
		reached := budgetMinutes
		for reached+step <= mins {
			reached += step
		}
		if mins >= budgetMinutes && reached > timeWarnedAt {
			timeWarnedAt = reached
			emitLineC(cYellow, fmt.Sprintf("  ⏱ session budget: %d min elapsed (ceiling %d) — good moment to /commit or wrap up",
				mins, budgetMinutes))
		}
	}
	if budgetKTokens > 0 {
		usedK := budgetTokensUsed() / 1000
		step := budgetKTokens / 2
		if step < 1 {
			step = 1
		}
		reached := budgetKTokens
		for reached+step <= usedK {
			reached += step
		}
		if usedK >= budgetKTokens && reached > tokWarnedAt {
			tokWarnedAt = reached
			emitLineC(cYellow, fmt.Sprintf("  ⏱ session budget: ~%dk tokens generated (ceiling %dk) — consider /compact or a fresh session",
				usedK, budgetKTokens))
		}
	}
}

// printBudget implements /budget.
func printBudget() { fmt.Print(renderBudget()) }

func renderBudget() string {
	var b strings.Builder
	if budgetMinutes == 0 && budgetKTokens == 0 {
		b.WriteString("no session budget set — config \"budget_minutes\" and/or \"budget_ktokens\" enable warnings\n")
	}
	if !sessionStart.IsZero() {
		mins := time.Since(sessionStart).Minutes()
		if budgetMinutes > 0 {
			fmt.Fprintf(&b, "time:   %.0f min / %d min ceiling (%.0f%%)\n", mins, budgetMinutes, 100*mins/float64(budgetMinutes))
		} else {
			fmt.Fprintf(&b, "time:   %.0f min (no ceiling)\n", mins)
		}
	}
	usedK := budgetTokensUsed() / 1000
	if budgetKTokens > 0 {
		fmt.Fprintf(&b, "tokens: ~%dk / %dk ceiling (%d%%) generated+reasoning\n", usedK, budgetKTokens, 100*usedK/budgetKTokens)
	} else {
		fmt.Fprintf(&b, "tokens: ~%dk generated+reasoning (no ceiling)\n", usedK)
	}
	return b.String()
}
