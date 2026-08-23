package tui

import (
	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIApplyEventBuildsTranscript(t *testing.T) {
	m := newTUIModel(func(string) {})
	m.applyEvent(core.Event{Kind: core.EvUser, Text: "hi"}) // user events aren't added by applyEvent (added on submit)
	m.applyEvent(core.Event{Kind: core.EvToolCall, Tool: "read_file", Text: `{"path":"x"}`})
	m.applyEvent(core.Event{Kind: core.EvAssistant, Text: "line one"})
	m.applyEvent(core.Event{Kind: core.EvAssistant, Text: "line two"})
	m.applyEvent(core.Event{Kind: core.EvError, Text: "boom"})

	joined := strings.Join(m.lines, "|")
	if !strings.Contains(joined, "read_file") {
		t.Fatalf("tool call missing from transcript: %q", joined)
	}
	if !strings.Contains(joined, "boom") {
		t.Fatalf("error missing: %q", joined)
	}
	// Consecutive assistant lines coalesce into one entry.
	asstCount := 0
	for _, l := range m.lines {
		if strings.HasPrefix(l, "\x00asst") {
			asstCount++
		}
	}
	if asstCount != 1 {
		t.Fatalf("assistant lines should coalesce into 1 entry, got %d", asstCount)
	}
}

func TestTUIThinkingUpdatesIndicator(t *testing.T) {
	m := newTUIModel(func(string) {})
	m.applyEvent(core.Event{Kind: core.EvThinking, Text: "thinking (~500 tokens)"})
	if m.thinking != "thinking (~500 tokens)" {
		t.Fatalf("thinking indicator not set: %q", m.thinking)
	}
}

func TestTUIEnterSubmitsAndDisablesInput(t *testing.T) {
	var submitted string
	m := newTUIModel(func(s string) { submitted = s })
	m.width, m.height = 100, 30 // so Update doesn't divide by zero on sizes
	m.input.SetValue("do the thing")
	// Simulate Enter.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	nm := updated.(tuiModel)
	if !nm.busy {
		t.Fatal("model should be busy after submit")
	}
	// submit runs in a goroutine; give it a beat isn't reliable, so just
	// assert the input was consumed and busy flipped.
	if nm.input.Value() != "" {
		t.Fatalf("input should reset after submit, got %q", nm.input.Value())
	}
	_ = submitted
}

func TestTUIQuitKeys(t *testing.T) {
	m := newTUIModel(func(string) {})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c should return a quit command")
	}
}

func TestTUIHistoryRecall(t *testing.T) {
	h := &tuiHistory{}
	h.add("first")
	h.add("second")
	h.add("third")
	// up from a fresh draft walks backwards
	if got := h.up("draft"); got != "third" {
		t.Fatalf("up1=%q want third", got)
	}
	if got := h.up(""); got != "second" {
		t.Fatalf("up2=%q want second", got)
	}
	if got := h.up(""); got != "first" {
		t.Fatalf("up3=%q want first", got)
	}
	if got := h.up(""); got != "first" {
		t.Fatalf("up past oldest should stay: %q", got)
	}
	// down walks forward and restores the stashed draft at the end
	h.down() // second
	h.down() // third
	if got := h.down(); got != "draft" {
		t.Fatalf("down past newest should restore draft, got %q", got)
	}
}

func TestTUIHistorySkipsDuplicates(t *testing.T) {
	h := &tuiHistory{}
	h.add("x")
	h.add("x")
	if len(h.entries) != 1 {
		t.Fatalf("immediate dup should be skipped: %v", h.entries)
	}
	h.add("")
	if len(h.entries) != 1 {
		t.Fatalf("blank should be skipped: %v", h.entries)
	}
}

func TestClassifyInput(t *testing.T) {
	cases := []struct {
		in   string
		kind agent.InputKind
		arg  string
	}{
		{"hello world", agent.InputPrompt, "hello world"},
		{"!ls -la", agent.InputShell, "ls -la"},
		{"@main.go", agent.InputFile, "main.go"},
		{"/stats", agent.InputCommand, "/stats"},
		{"   ", agent.InputBlank, ""},
		{"", agent.InputBlank, ""},
	}
	for _, c := range cases {
		k, a := agent.ClassifyInput(c.in)
		if k != c.kind || a != c.arg {
			t.Fatalf("classify(%q)=(%d,%q) want (%d,%q)", c.in, k, a, c.kind, c.arg)
		}
	}
}

func TestMultilineToggle(t *testing.T) {
	if !isMultilineToggle(`"""`) {
		t.Fatal(`""" should toggle multiline`)
	}
	if !isMultilineToggle(`  """  `) {
		t.Fatal("whitespace-padded triple-quote should toggle")
	}
	if isMultilineToggle(`""" and more`) {
		t.Fatal("triple-quote with trailing text is not a toggle")
	}
	if isMultilineToggle("normal line") {
		t.Fatal("normal line is not a toggle")
	}
}

func TestTUIBrowserPromptStartsTurn(t *testing.T) {
	var submitted string
	m := newTUIModel(func(s string) { submitted = s })
	m.width, m.height = 100, 30
	updated, _ := m.Update(browserPromptMsg{text: "prompt from browser"})
	nm := updated.(tuiModel)
	if !nm.busy {
		t.Fatal("browser prompt should start a turn (busy=true)")
	}
	_ = submitted
	// A browser prompt while busy is ignored (no crash, no double-run).
	updated2, _ := nm.Update(browserPromptMsg{text: "second while busy"})
	nm2 := updated2.(tuiModel)
	if !nm2.busy {
		t.Fatal("should still be busy")
	}
}
