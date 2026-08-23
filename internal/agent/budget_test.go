package agent

import (
	"testing"
	"time"
)

func resetBudget() {
	stats.mu.Lock()
	stats.genTk, stats.thinkTk = 0, 0
	stats.mu.Unlock()
	budgetMinutes, budgetKTokens = 0, 0
	timeWarnedAt, tokWarnedAt = 0, 0
	sessionStart = time.Time{}
}

func TestBudgetTokenThreshold(t *testing.T) {
	resetBudget()
	defer resetBudget()
	budgetKTokens = 10
	stats.mu.Lock()
	stats.genTk, stats.thinkTk = 6000, 5000 // 11k > 10k
	stats.mu.Unlock()

	checkBudget()
	if tokWarnedAt != 10 {
		t.Fatalf("first crossing must warn at 10k, got %d", tokWarnedAt)
	}
	// Same level again: no re-warn.
	prev := tokWarnedAt
	checkBudget()
	if tokWarnedAt != prev {
		t.Fatal("must not re-warn at the same threshold")
	}
	// Cross 150% (15k): re-warn steps to 15.
	stats.mu.Lock()
	stats.genTk = 11000 // total 16k
	stats.mu.Unlock()
	checkBudget()
	if tokWarnedAt != 15 {
		t.Fatalf("must re-warn at 150%%, got %d", tokWarnedAt)
	}
}

func TestBudgetTimeThreshold(t *testing.T) {
	resetBudget()
	defer resetBudget()
	budgetMinutes = 30
	sessionStart = time.Now().Add(-31 * time.Minute)
	checkBudget()
	if timeWarnedAt != 30 {
		t.Fatalf("must warn past the time ceiling, got %d", timeWarnedAt)
	}
}

func TestBudgetOffByDefault(t *testing.T) {
	resetBudget()
	defer resetBudget()
	sessionStart = time.Now().Add(-10 * time.Hour)
	stats.mu.Lock()
	stats.genTk = 9_999_999
	stats.mu.Unlock()
	checkBudget() // both ceilings zero → nothing warned
	if timeWarnedAt != 0 || tokWarnedAt != 0 {
		t.Fatal("unset budgets must never warn")
	}
}

func TestBudgetTokensUsed(t *testing.T) {
	resetBudget()
	defer resetBudget()
	stats.mu.Lock()
	stats.genTk, stats.thinkTk, stats.promptTk = 100, 250, 999999
	stats.mu.Unlock()
	if got := budgetTokensUsed(); got != 350 {
		t.Fatalf("budget counts gen+think only (not prompt): got %d", got)
	}
}
