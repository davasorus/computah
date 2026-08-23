package agent

import (
	"strings"
	"testing"
)

func TestSessionCost(t *testing.T) {
	// Save/restore globals so the test is isolated.
	inSaved, outSaved := priceInPerM, priceOutPerM
	defer func() { priceInPerM, priceOutPerM = inSaved, outSaved }()

	s := &statsRecorder{requests: 3, promptTk: 1_000_000, genTk: 500_000, thinkTk: 500_000}

	// No pricing → cost is 0 and pricing is disabled.
	priceInPerM, priceOutPerM = 0, 0
	if pricingEnabled() {
		t.Error("pricing should be disabled when both rates are 0")
	}
	if c := s.sessionCostUSD(); c != 0 {
		t.Errorf("cost should be 0 with no pricing, got %v", c)
	}

	// $3/M in, $15/M out. 1M in = $3; 1M out (500k gen + 500k think) = $15. Total $18.
	priceInPerM, priceOutPerM = 3.0, 15.0
	if !pricingEnabled() {
		t.Error("pricing should be enabled when a rate is set")
	}
	if c := s.sessionCostUSD(); c < 17.999 || c > 18.001 {
		t.Errorf("expected ~$18.00, got %v", c)
	}

	// The rendered report should include the cost line only when priced.
	if !strings.Contains(s.render(), "estimated cost") {
		t.Error("render should include cost line when pricing is on")
	}
	priceInPerM, priceOutPerM = 0, 0
	if strings.Contains(s.render(), "estimated cost") {
		t.Error("render must NOT include cost line for local/free (no pricing)")
	}
}
