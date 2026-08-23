package agent

import "testing"

func TestModelForTurn(t *testing.T) {
	// Save/restore globals so the test is isolated.
	cm, pm, fm, plan := curModel, planModel, fastModel, planMode
	defer func() { curModel, planModel, fastModel, planMode = cm, pm, fm, plan }()

	curModel = "main-model"
	planModel = ""
	fastModel = ""
	planMode = false

	// Nothing configured: every turn uses the main model.
	if got := modelForTurn("do a big refactor"); got != "main-model" {
		t.Errorf("default should be main model, got %q", got)
	}

	// Plan mode with a plan model: use it (even for a trivial-looking input —
	// planning is never trivial).
	planMode = true
	planModel = "big-plan-model"
	if got := modelForTurn("yes"); got != "big-plan-model" {
		t.Errorf("plan turn should use planModel, got %q", got)
	}
	// Plan mode, no plan model: fall back to main.
	planModel = ""
	if got := modelForTurn("yes"); got != "main-model" {
		t.Errorf("plan turn w/o planModel should fall back, got %q", got)
	}

	// Execution mode with a fast model configured.
	planMode = false
	fastModel = "fast-model"
	// Trivial follow-up → fast model.
	if got := modelForTurn("continue"); got != "fast-model" {
		t.Errorf("trivial turn should use fastModel, got %q", got)
	}
	// Substantive turn → main model, even with a fast model set.
	if got := modelForTurn("refactor the auth layer to use interfaces"); got != "main-model" {
		t.Errorf("substantive turn should use main model, got %q", got)
	}
	// Fast model unset → trivial turn still uses main.
	fastModel = ""
	if got := modelForTurn("continue"); got != "main-model" {
		t.Errorf("no fastModel means main model, got %q", got)
	}
}

func TestIsTrivialTurn(t *testing.T) {
	trivial := []string{
		"yes", "y", "continue", "go ahead", "do it", "next", "ok",
		"commit", "commit that change", "run the tests", "push", "fix that",
		"try again", "  Continue  ", "YES",
	}
	for _, s := range trivial {
		if !isTrivialTurn(s) {
			t.Errorf("expected %q to be trivial", s)
		}
	}
	substantive := []string{
		"",
		"refactor the auth layer to use interfaces and update all callers",
		"why does the build fail on windows but not linux",
		"add a new endpoint that validates the payload and writes to postgres",
		"commit that change and also refactor the entire session store to be thread safe with a mutex", // long → not trivial
		"line one\nline two", // multi-line → not trivial
	}
	for _, s := range substantive {
		if isTrivialTurn(s) {
			t.Errorf("expected %q to be substantive (not trivial)", s)
		}
	}
}
