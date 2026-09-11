package agent

import (
	"strings"
	"testing"
)

func mkConvo() []Message {
	msgs := []Message{{Role: "system", Content: "SYSPROMPT"}}
	msgs = append(msgs, Message{Role: "user", Content: "refactor proj9 to dependency injection"}) // the goal
	// A middle turn full of tool traffic that must all be elided.
	call := Message{Role: "assistant"}
	call.ToolCalls = []ToolCall{{ID: "1"}}
	call.ToolCalls[0].Function.Name = "read_file"
	call.ToolCalls[0].Function.Arguments = `{"path":"db.go"}`
	msgs = append(msgs, call,
		Message{Role: "tool", ToolCallID: "1", Content: "huge stale file content " + strings.Repeat("x", 5000)},
		Message{Role: "assistant", Content: "I refactored the Database package."},
		Message{Role: "user", Content: "[The user interrupted this turn.]"}, // synthetic: elided
	)
	for i := 0; i < 15; i++ {
		msgs = append(msgs,
			Message{Role: "user", Content: "intermediate question " + strings.Repeat("q", 400)},
			Message{Role: "assistant", Content: "intermediate answer " + strings.Repeat("a", 400)},
		)
	}
	msgs = append(msgs, Message{Role: "user", Content: "now fix the repository mapping"})
	return msgs
}

func TestDistillInvariants(t *testing.T) {
	sb := &Sandbox{Root: t.TempDir(), Modified: []string{"todo/repository.go", "todo/repository.go", "Database/db.go"}}
	oldTodos := todos
	todos = []todoItem{{Text: "map ListItems", Done: true}, {Text: "map SaveItems"}}
	defer func() { todos = oldTodos }()

	wire := distill(mkConvo(), sb)

	if wire[0].Content != "SYSPROMPT" {
		t.Fatal("system prompt must lead")
	}
	for _, m := range wire {
		if m.Role == "tool" {
			t.Fatal("wire must contain no tool results")
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			t.Fatal("wire must contain no tool-calling assistant messages")
		}
		if strings.Contains(m.Content, "stale file content") {
			t.Fatal("stale tool output leaked into the wire")
		}
	}
	last := wire[len(wire)-1]
	if last.Role != "user" || last.Content != "now fix the repository mapping" {
		t.Fatalf("newest instruction must be last: %+v", last)
	}
	joined := ""
	for _, m := range wire {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "dependency injection") {
		t.Fatal("session goal must survive the window sliding past it")
	}
	if !strings.Contains(joined, "[Session state]") || !strings.Contains(joined, "todo/repository.go, Database/db.go") {
		t.Fatal("state block must list deduped modified files")
	}
	if !strings.Contains(joined, "[x] map ListItems") || !strings.Contains(joined, "[ ] map SaveItems") {
		t.Fatal("checklist must be in the state block")
	}
	if !strings.Contains(joined, "re-read files") {
		t.Fatal("elision note missing")
	}
	if strings.Contains(joined, "interrupted this turn") {
		t.Fatal("synthetic [bracket] user messages must be elided")
	}
	if estimateTokens(wire) > distillBudget+2500 { // budget + system/state overhead
		t.Fatalf("wire too large: ~%d tokens", estimateTokens(wire))
	}
	if estimateTokens(wire) >= estimateTokens(mkConvo()) {
		t.Fatal("distilled wire must be smaller than the canonical transcript")
	}
}

func TestDistillRespectsConfigOverrides(t *testing.T) {
	oldBudget, oldWindow := distillBudget, contextV2Window
	defer func() { distillBudget, contextV2Window = oldBudget, oldWindow }()

	// A tiny window/budget must trim the recent-dialogue window hard,
	// proving these package vars (set from Config in main.go) are actually
	// consulted rather than a hardcoded literal.
	distillBudget = 50
	contextV2Window = 2

	sb := &Sandbox{Root: t.TempDir()}
	wire := distill(mkConvo(), sb)

	recent := 0
	for _, m := range wire {
		if m.Role == "user" || m.Role == "assistant" {
			recent++
		}
	}
	if recent > contextV2Window+2 { // +2 slack: goal line + state block aren't part of the trimmed window
		t.Fatalf("distill did not respect overridden contextV2Window=%d: got %d dialogue messages", contextV2Window, recent)
	}
}

func TestDistillShortSessionPassthrough(t *testing.T) {
	msgs := []Message{{Role: "system", Content: "S"}, {Role: "user", Content: "hi"}}
	wire := distill(msgs, &Sandbox{Root: t.TempDir()})
	if wire[len(wire)-1].Content != "hi" {
		t.Fatal("short sessions must keep the instruction last")
	}
}
