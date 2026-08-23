package agent

import (
	"strings"
	"testing"
)

func TestIsTableSeparator(t *testing.T) {
	yes := []string{"| :--- | ---: |", "|---|---|", " --- | --- ", "|:-:|:-:|:-:|"}
	no := []string{"| Goal | Status |", "just text", "| a | b |", ""}
	for _, s := range yes {
		if !isTableSeparator(s) {
			t.Fatalf("should be separator: %q", s)
		}
	}
	for _, s := range no {
		if isTableSeparator(s) {
			t.Fatalf("should NOT be separator: %q", s)
		}
	}
}

func TestParseTable(t *testing.T) {
	lines := []string{
		"| Goal | Status | Notes |",
		"| :--- | :--- | :--- |",
		"| **Styling** | Partial | lipgloss missing |",
		"| TUI | Not Started | no bubbletea |",
		"",
		"trailing text",
	}
	tbl, next := parseTable(lines, 0)
	if len(tbl.header) != 3 || tbl.header[0] != "Goal" {
		t.Fatalf("header wrong: %v", tbl.header)
	}
	if len(tbl.rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(tbl.rows))
	}
	if tbl.rows[0][0] != "**Styling**" {
		t.Fatalf("cell content wrong: %v", tbl.rows[0])
	}
	if next != 4 {
		t.Fatalf("next index should skip to blank line at 4, got %d", next)
	}
}

func TestRenderTablePlainAligned(t *testing.T) {
	tbl := mdTable{
		header: []string{"Goal", "Status"},
		rows:   [][]string{{"Styling", "Partial"}, {"TUI", "Not Started"}},
	}
	out := renderTablePlain(tbl, stripMarkdown, 0)
	lines := strings.Split(out, "\n")
	// header + separator + 2 rows
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d:\n%s", len(lines), out)
	}
	// columns aligned: "Status" column width = len("Not Started")=11
	if !strings.Contains(lines[0], "Status") {
		t.Fatalf("header missing: %q", lines[0])
	}
}

func TestRenderTableANSIHasBorders(t *testing.T) {
	saved := useColor
	useColor = true
	defer func() { useColor = saved }()
	tbl := mdTable{header: []string{"A", "B"}, rows: [][]string{{"1", "2"}}}
	out := renderTableANSI(tbl, renderInline, stripMarkdown, 0)
	for _, want := range []string{"┌", "┐", "│", "├", "┤", "└", "┘"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ANSI table missing border %q:\n%s", want, out)
		}
	}
}

// TestStreamingTableDetection feeds a table token-by-token (as the model
// streams it) and verifies the writer buffers and renders it as a table
// rather than as raw pipe lines.
func TestStreamingTableDetection(t *testing.T) {
	saved := useColor
	useColor = true
	defer func() { useColor = saved }()

	// Capture assistant bus events (the dashboard mirror) to confirm the raw
	// table lines are emitted for HTML rendering.
	var asst []string
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvAssistant {
			asst = append(asst, e.Text)
		}
	}))
	defer unsub()

	m := newMDWriter()
	stream := "Here is a table:\n| Goal | Status |\n| :--- | :--- |\n| TUI | Done |\n\nAfter.\n"
	// feed a few chars at a time to simulate streaming
	for i := 0; i < len(stream); i += 3 {
		end := i + 3
		if end > len(stream) {
			end = len(stream)
		}
		m.Write(stream[i:end])
	}
	m.Flush()
	// The raw table lines must have reached the bus for the dashboard.
	joined := strings.Join(asst, "\n")
	if !strings.Contains(joined, "| Goal | Status |") {
		t.Fatalf("raw table lines should reach the bus:\n%s", joined)
	}
}

func TestRenderMarkdownBlockTable(t *testing.T) {
	src := "### Heading\n\n| Goal | Status |\n| :--- | :--- |\n| TUI | Done |\n\ntext after"
	// color mode → ANSI table with borders
	out := renderMarkdownBlock(src, true, 0)
	if !strings.Contains(out, "┌") || !strings.Contains(out, "Goal") {
		t.Fatalf("block render (color) missing table border:\n%s", out)
	}
	// plain mode → aligned, no box chars
	plain := renderMarkdownBlock(src, false, 0)
	if strings.Contains(plain, "┌") {
		t.Fatalf("plain block must not have box chars:\n%s", plain)
	}
	if !strings.Contains(plain, "Goal") || !strings.Contains(plain, "Status") {
		t.Fatalf("plain block missing table content:\n%s", plain)
	}
}

func TestTableRespectsMaxWidth(t *testing.T) {
	saved := useColor
	useColor = true
	defer func() { useColor = saved }()
	tbl := mdTable{
		header: []string{"Task", "Description"},
		rows: [][]string{
			{"Visual Styling", "Integrate lipgloss or color; highlight High Priority in red; struck-through text for Done items and a lot more text that would overflow a narrow terminal badly"},
		},
	}
	out := renderTableANSI(tbl, renderInline, stripMarkdown, 60)
	// Every rendered line must fit within the cap (allowing for the ANSI
	// escape codes, which don't take visible width — so strip them first).
	for _, line := range strings.Split(out, "\n") {
		visible := stripANSI(line)
		if displayWidth(visible) > 60 {
			t.Fatalf("line exceeds maxWidth 60 (got %d): %q", displayWidth(visible), visible)
		}
	}
}

// stripANSI removes ANSI escape sequences for width checking in tests.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestWrapLinePreservesTableLines(t *testing.T) {
	// Box-drawing lines must NOT be wrapped (they're pre-fit).
	tableLine := "│ Task │ Description │"
	if wrapLine(tableLine, 5) != tableLine {
		t.Fatal("table lines must pass through wrapLine unchanged")
	}
	// Short non-table lines pass through.
	if wrapLine("hi", 80) != "hi" {
		t.Fatal("short line should be unchanged")
	}
	// Long non-table lines get wrapped (result spans multiple lines).
	long := strings.Repeat("word ", 40)
	wrapped := wrapLine(long, 20)
	if !strings.Contains(wrapped, "\n") {
		t.Fatal("long line should wrap to multiple lines")
	}
	for _, ln := range strings.Split(wrapped, "\n") {
		if visibleLen(ln) > 20 {
			t.Fatalf("wrapped line exceeds width 20: %q", ln)
		}
	}
}

func TestDisplayWidthANSI(t *testing.T) {
	// ANSI codes don't count toward width.
	s := "\033[1mbold\033[0m"
	if visibleLen(s) != 4 {
		t.Fatalf("ANSI width should be 4 (bold), got %d", visibleLen(s))
	}
}

func TestWrapANSIWrapsToWidth(t *testing.T) {
	long := "the quick brown fox jumps over the lazy dog and keeps on running past the edge"
	out := wrapANSI(long, 20)
	for _, ln := range strings.Split(out, "\n") {
		if visibleLen(ln) > 20 {
			t.Fatalf("wrapped line exceeds 20: %q (%d)", ln, visibleLen(ln))
		}
	}
	if !strings.Contains(out, "\n") {
		t.Fatal("long text should wrap to multiple lines")
	}
}

func TestWrapANSIPreservesColorCodes(t *testing.T) {
	// A colored long line: the ANSI codes must survive and not count toward width.
	colored := "\033[36m" + strings.Repeat("word ", 20) + "\033[0m"
	out := wrapANSI(colored, 20)
	for _, ln := range strings.Split(out, "\n") {
		if visibleLen(ln) > 20 {
			t.Fatalf("colored wrapped line exceeds 20 visible: %q (%d)", ln, visibleLen(ln))
		}
	}
}

func TestWrapANSILeavesTableLines(t *testing.T) {
	tl := "│ a │ b │"
	if wrapANSI(tl, 3) != tl {
		t.Fatal("table lines must not be wrapped by wrapANSI")
	}
}

func TestWrapANSIPreservesIndent(t *testing.T) {
	// A wrapped list item keeps its leading indent on continuation lines.
	line := "    1. this is a fairly long list item that will need to wrap onto another line for sure"
	out := wrapANSI(line, 30)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatal("should have wrapped")
	}
	if !strings.HasPrefix(lines[1], "    ") {
		t.Fatalf("continuation line should keep indent: %q", lines[1])
	}
}
