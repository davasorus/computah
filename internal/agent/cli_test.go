package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- markdown renderer ----------

func TestMDInlineStyles(t *testing.T) {
	old := useColor
	useColor = true
	defer func() { useColor = old }()
	m := newMDWriter()
	out := m.renderLine("use **bold** and *italic* and `code` here")
	for _, want := range []string{"\033[1mbold\033[22m", "\033[3mitalic\033[23m", "\033[36mcode\033[0m"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	// Unbalanced markers left alone: a lone * is multiplication.
	out = m.renderLine("x * y = z")
	if strings.Contains(out, "\033[3m") {
		t.Fatalf("lone asterisk must not italicize: %q", out)
	}
}

func TestMDFenceProtectsContent(t *testing.T) {
	old := useColor
	useColor = true
	defer func() { useColor = old }()
	m := newMDWriter()
	open := m.renderLine("```go")
	if !strings.Contains(open, "go") || !m.inFence {
		t.Fatalf("fence open failed: %q", open)
	}
	code := m.renderLine(`s := "**not bold**"`)
	if strings.Contains(code, "\033[1m") {
		t.Fatalf("emphasis must not apply inside fences: %q", code)
	}
	m.renderLine("```")
	if m.inFence {
		t.Fatal("fence must close")
	}
}

func TestMDStreamingAcrossTokens(t *testing.T) {
	// Tokens split mid-line and mid-marker must still assemble correctly —
	// this is the whole reason the renderer is line-buffered.
	old := useColor
	useColor = true
	defer func() { useColor = old }()
	m := newMDWriter()
	for _, tok := range []string{"he", "llo **wo", "rld**"} {
		m.Write(tok) // no newline yet: nothing printed, no panic
	}
	if m.buf.String() != "hello **world**" {
		t.Fatalf("buffer misassembled: %q", m.buf.String())
	}
}

// ---------- @file expansion ----------

func TestExpandFileRefs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "add.go"), []byte("package cmd // position bug here"), 0o644)
	msg, attached := expandFileRefs("fix the bug in @add.go, please", dir)
	if len(attached) != 1 || attached[0] != "add.go" {
		t.Fatalf("attach failed: %v", attached)
	}
	if !strings.Contains(msg, "position bug here") || !strings.Contains(msg, "content of @add.go") {
		t.Fatalf("content not inlined: %q", msg)
	}
	if !strings.HasPrefix(msg, "fix the bug in @add.go, please") {
		t.Fatal("original message must stay intact")
	}
}

func TestExpandFileRefsIgnoresNonFiles(t *testing.T) {
	dir := t.TempDir()
	msg, attached := expandFileRefs("email me @davasorus and check @nosuch.go", dir)
	if len(attached) != 0 {
		t.Fatalf("nothing should attach: %v", attached)
	}
	if msg != "email me @davasorus and check @nosuch.go" {
		t.Fatalf("message must be unchanged: %q", msg)
	}
}

func TestExpandFileRefsTooLarge(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "big.log"), []byte(strings.Repeat("x", 70*1024)), 0o644)
	msg, attached := expandFileRefs("look at @big.log", dir)
	if len(attached) != 1 || !strings.Contains(attached[0], "too large") {
		t.Fatalf("large file must be flagged, not inlined: %v", attached)
	}
	if strings.Contains(msg, strings.Repeat("x", 1024)) {
		t.Fatal("large file content must not be inlined")
	}
}

// ---------- completion ----------

func TestCompleteLineCommands(t *testing.T) {
	line, pos, ok := completeLine("/ver", 4, '\t')
	if !ok || line != "/verify" || pos != len("/verify") {
		t.Fatalf("got %q pos=%d ok=%v", line, pos, ok)
	}
	// Multiple matches extend to common prefix only.
	_, _, ok = completeLine("/c", 2, '\t')
	if ok { // /compact /context /copy share only "/co"
		l, p, _ := completeLine("/c", 2, '\t')
		if l != "/co" || p != 3 {
			t.Fatalf("common prefix expected, got %q pos=%d", l, p)
		}
	}
}

func TestCompleteLinePaths(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cmd"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)
	oldRoot := completerRoot
	completerRoot = dir
	defer func() { completerRoot = oldRoot }()

	line, pos, ok := completeLine("fix cm", 6, '\t')
	if !ok || line != "fix cmd"+string(filepath.Separator) {
		t.Fatalf("dir completion: got %q ok=%v", line, ok)
	}
	if pos != len(line) {
		t.Fatalf("cursor must land at end, got %d", pos)
	}
	line, _, ok = completeLine("/undo mai", 9, '\t')
	if !ok || line != "/undo main.go" {
		t.Fatalf("file completion after command: got %q ok=%v", line, ok)
	}
	_, _, ok = completeLine("fix zzz", 7, '\t')
	if ok {
		t.Fatal("no candidates must leave the line alone")
	}
}

// ---------- persistent history ----------

func TestFileHistoryPersistsAcrossInstances(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := newFileHistory()
	h.Add("first command")
	h.Add("second command")
	h.Add("second command") // immediate duplicate skipped
	h.Add("multi\nline")    // stored space-joined

	h2 := newFileHistory() // fresh instance = new session
	if h2.Len() != 3 {
		t.Fatalf("want 3 entries after reload, got %d", h2.Len())
	}
	if h2.At(0) != "multi line" || h2.At(2) != "first command" {
		t.Fatalf("order wrong: newest=%q oldest=%q", h2.At(0), h2.At(2))
	}
}

func TestFileHistoryBounded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := newFileHistory()
	for i := 0; i < maxHistory+50; i++ {
		h.Add(strings.Repeat("x", 3) + string(rune('a'+i%26)) + strings.Repeat("y", i%7) + " " + strings.Repeat("z", 1) + " " + string(rune('0'+i%10)) + " " + strings.Repeat("q", 1) + " " + string(rune('A'+i%26)) + " " + string(rune('a'+(i/26)%26)) + string(rune('a'+i%26)))
	}
	if h.Len() > maxHistory {
		t.Fatalf("history unbounded: %d", h.Len())
	}
}

// ---------- status line ----------

func TestStatusLine(t *testing.T) {
	old := useColor
	useColor = false // plain text for assertions
	defer func() { useColor = old }()
	msgs := []Message{{Role: "system", Content: strings.Repeat("x", 8000)}}
	s := statusLine("google/gemma-4-12b", msgs, "/home/x/proj9")
	if !strings.Contains(s, "gemma-4-12b") || !strings.Contains(s, "proj9") || !strings.Contains(s, "k/") {
		t.Fatalf("status line missing pieces: %q", s)
	}
}

// ---------- pty: Tab completion + history persistence end to end ----------

func TestPTYTabCompletion(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("/ver\t\r") }()
		r := read("> ")
		if r.s != "/verify" {
			t.Fatalf("tab completion through the editor failed: %q", r.s)
		}
	})
}

func TestPTYHistorySurvivesSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Session one: type a command.
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("go build ./...\r") }()
		if r := read("> "); r.s != "go build ./..." {
			t.Fatalf("setup: %q", r.s)
		}
	})
	// Session two (fresh terminal = fresh process, same HOME): up-arrow
	// must recall it from ~/.agent/history.
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("\x1b[A\r") }()
		r := read("> ")
		if r.s != "go build ./..." {
			t.Fatalf("history did not survive the session boundary: %q", r.s)
		}
	})
}

// ---------- closest-lines feedback on failed edits ----------

func TestEditRejectionShowsClosestLines(t *testing.T) {
	dir := t.TempDir()
	// The real transcript failure: tab-indented "Port     String" in the
	// file, model searching for a version with a trailing comma.
	content := "type Config struct {\n\tHost     string\n\tPort     String\n\tUser     string\n}\n"
	os.WriteFile(filepath.Join(dir, "db.go"), []byte(content), 0o644)
	sb := &Sandbox{Root: dir}
	out := sb.Execute("edit_file", map[string]any{
		"path":    "db.go",
		"old_str": "\tPort     String,\n", // comma that isn't in the file
		"new_str": "\tPort     string,\n",
	})
	if !strings.HasPrefix(out, "ERROR") {
		t.Fatalf("must reject: %q", out)
	}
	if !strings.Contains(out, `"\tPort     String"`) {
		t.Fatalf("error must show the actual line byte-exact (tab visible, no comma): %q", out)
	}
	if !strings.Contains(out, "line 3") {
		t.Fatalf("error must include the line number: %q", out)
	}
}

func TestEditRejectionWhenTokenAbsent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n"), 0o644)
	sb := &Sandbox{Root: dir}
	out := sb.Execute("edit_file", map[string]any{
		"path":    "a.go",
		"old_str": "NonexistentSymbol here",
		"new_str": "x",
	})
	if !strings.Contains(out, "re-read the file") && !strings.Contains(out, "resembles") {
		t.Fatalf("absent token must suggest re-reading: %q", out)
	}
}

func TestClosestLinesPicksDistinctiveToken(t *testing.T) {
	got := closestLines("aaa\n\tPort     String\nbbb\n", "\tPort     String,\n")
	if !strings.Contains(got, "line 2") || !strings.Contains(got, `\t`) {
		t.Fatalf("got %q", got)
	}
}
