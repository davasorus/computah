package agent

import "testing"

func TestModelForTurn(t *testing.T) {
	// Save/restore globals so the test is isolated.
	cm, pm, plan := curModel, planModel, planMode
	defer func() { curModel, planModel, planMode = cm, pm, plan }()

	curModel = "fast-exec-model"

	// Plan mode OFF: always the main model, regardless of planModel.
	planMode = false
	planModel = "big-plan-model"
	if got := modelForTurn(); got != "fast-exec-model" {
		t.Errorf("execution turn should use curModel, got %q", got)
	}

	// Plan mode ON with a plan model configured: use the plan model.
	planMode = true
	if got := modelForTurn(); got != "big-plan-model" {
		t.Errorf("plan turn should use planModel, got %q", got)
	}

	// Plan mode ON but NO plan model: fall back to the main model.
	planModel = ""
	if got := modelForTurn(); got != "fast-exec-model" {
		t.Errorf("plan turn with no planModel should fall back to curModel, got %q", got)
	}
}
