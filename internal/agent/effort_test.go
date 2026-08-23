package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEffortSelection(t *testing.T) {
	oldR, oldP, oldPlan := reasoningEffort, planReasoningEffort, planMode
	defer func() { reasoningEffort, planReasoningEffort, planMode = oldR, oldP, oldPlan }()
	reasoningEffort, planReasoningEffort = "low", "high"
	planMode = false
	if currentReasoningEffort() != "low" {
		t.Fatal("normal turns use the base effort")
	}
	planMode = true
	if currentReasoningEffort() != "high" {
		t.Fatal("plan mode uses its own effort")
	}
	planReasoningEffort = ""
	if currentReasoningEffort() != "low" {
		t.Fatal("plan effort unset falls back to base")
	}
	reasoningEffort = ""
	planMode = false
	req := ChatRequest{Model: "m", ReasoningEffort: currentReasoningEffort()}
	data, _ := json.Marshal(req)
	if strings.Contains(string(data), "reasoning_effort") {
		t.Fatalf("unset effort must omit the field entirely: %s", data)
	}
}
