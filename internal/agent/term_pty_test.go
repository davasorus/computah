package agent

// These tests drive the REAL TTY input path through an actual pseudo-
// terminal (creack/pty): raw mode, escape sequences, the x/term line
// editor's history and bracketed-paste handling. Nothing is simulated —
// what passes here is what the terminal does.

import (
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

type lineResult struct {
	s  string
	ok bool
}

// withPTY points the TTY input path at a fresh pty pair for the duration of
// fn, handing fn the master side to type into.
func withPTY(t *testing.T, fn func(typeInto func(string), read func(prompt string) lineResult)) {
	t.Helper()
	ptmx, tts, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tts.Close()

	oldIn, oldOut, oldForce, oldTerm, oldWatch := ttyIn, ttyOut, forceTTY, uiTerm, uiWatch
	ttyIn, ttyOut, forceTTY, uiTerm, uiWatch = tts, tts, true, nil, nil
	defer func() { ttyIn, ttyOut, forceTTY, uiTerm, uiWatch = oldIn, oldOut, oldForce, oldTerm, oldWatch }()

	// Drain the master side so the editor's echo writes never block.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()

	typeInto := func(s string) {
		if _, err := ptmx.WriteString(s); err != nil {
			t.Errorf("pty write: %v", err)
		}
	}
	read := func(prompt string) lineResult {
		ch := make(chan lineResult, 1)
		go func() {
			s, ok := ttyReadLine(prompt)
			ch <- lineResult{s, ok}
		}()
		select {
		case r := <-ch:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("ttyReadLine hung — input never delivered through the pty")
			return lineResult{}
		}
	}
	fn(typeInto, read)
}

func TestPTYPlainLine(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("hello world\r") }()
		r := read("> ")
		if !r.ok || r.s != "hello world" {
			t.Fatalf("got %q ok=%v", r.s, r.ok)
		}
	})
}

func TestPTYUpArrowHistory(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("first command\r") }()
		if r := read("> "); r.s != "first command" {
			t.Fatalf("setup line: %q", r.s)
		}
		go func() { time.Sleep(50 * time.Millisecond); typeInto("second one\r") }()
		if r := read("> "); r.s != "second one" {
			t.Fatalf("setup line 2: %q", r.s)
		}
		// Up, Up = two entries back.
		go func() { time.Sleep(50 * time.Millisecond); typeInto("\x1b[A\x1b[A\r") }()
		if r := read("> "); r.s != "first command" {
			t.Fatalf("up-arrow x2 must recall the first command, got %q", r.s)
		}
		// Up alone recalls the most recent submitted line.
		go func() { time.Sleep(50 * time.Millisecond); typeInto("\x1b[A\r") }()
		if r := read("> "); r.s != "first command" {
			// history now has "first command" as most recent (it was resubmitted)
			t.Fatalf("up-arrow must recall last submitted, got %q", r.s)
		}
	})
}

func TestPTYLineEditing(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		// Type "helo", left-arrow over the 'o', insert 'l' → "hello".
		go func() { time.Sleep(50 * time.Millisecond); typeInto("helo\x1b[Dl\r") }()
		r := read("> ")
		if r.s != "hello" {
			t.Fatalf("left-arrow insert failed: %q", r.s)
		}
	})
}

func TestPTYBracketedPasteMultiline(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		paste := "\x1b[200~PS /mnt/z> go run . list\r0  one two todo\r0  three   todo\x1b[201~\r"
		go func() { time.Sleep(50 * time.Millisecond); typeInto(paste) }()
		r := read("> ")
		want := "PS /mnt/z> go run . list\n0  one two todo\n0  three   todo"
		if r.s != want {
			t.Fatalf("multi-line paste not assembled as one message:\n got %q\nwant %q", r.s, want)
		}
	})
}

func TestPTYPasteContainingTripleQuotes(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		// The exact failure `"""` mode can't survive: pasted content that
		// contains a """ line. Bracketed paste must deliver it intact.
		paste := "\x1b[200~def f():\r    \"\"\"docstring\"\"\"\r    return 1\x1b[201~\r"
		go func() { time.Sleep(50 * time.Millisecond); typeInto(paste) }()
		r := read("> ")
		if !strings.Contains(r.s, `"""docstring"""`) || !strings.Contains(r.s, "return 1") {
			t.Fatalf("paste with embedded triple quotes mangled: %q", r.s)
		}
		if strings.Count(r.s, "\n") != 2 {
			t.Fatalf("want 3 lines in one message, got %q", r.s)
		}
	})
}

func TestPTYCtrlD(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("\x04") }()
		r := read("> ")
		if r.ok {
			t.Fatalf("Ctrl+D must signal EOF, got %q ok=true", r.s)
		}
	})
}

func TestPTYCtrlCReprompts(t *testing.T) {
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		// Ctrl+C at the prompt must NOT quit — it clears and returns empty.
		go func() { time.Sleep(50 * time.Millisecond); typeInto("half typed\x03") }()
		r := read("> ")
		if !r.ok {
			t.Fatal("Ctrl+C must not read as EOF/quit")
		}
		if r.s != "" {
			t.Fatalf("Ctrl+C must clear the line, got %q", r.s)
		}
		// And the editor must still work afterwards.
		go func() { time.Sleep(50 * time.Millisecond); typeInto("still alive\r") }()
		if r := read("> "); r.s != "still alive" {
			t.Fatalf("editor broken after Ctrl+C: %q", r.s)
		}
	})
}

func TestPTYApplicationModeArrows(t *testing.T) {
	// Terminals in application cursor-key mode (Windows Terminal + zsh is a
	// common combo) send Up as ESC O A, not ESC [ A. This is exactly the
	// "pressing up gives me a literal A" failure.
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("recall me please\r") }()
		if r := read("> "); r.s != "recall me please" {
			t.Fatalf("setup: %q", r.s)
		}
		go func() { time.Sleep(50 * time.Millisecond); typeInto("\x1bOA\r") }()
		r := read("> ")
		if r.s == "A" {
			t.Fatal("SS3 up-arrow inserted a literal A — translation failed")
		}
		if r.s != "recall me please" {
			t.Fatalf("SS3 up-arrow must recall history, got %q", r.s)
		}
	})
}

func TestPTYSplitEscapeSequence(t *testing.T) {
	// The ESC and the rest of the sequence arriving in separate reads must
	// not desynchronize the translator.
	withPTY(t, func(typeInto func(string), read func(string) lineResult) {
		go func() { time.Sleep(50 * time.Millisecond); typeInto("history line\r") }()
		if r := read("> "); r.s != "history line" {
			t.Fatalf("setup: %q", r.s)
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			typeInto("\x1b")
			time.Sleep(30 * time.Millisecond) // force a read boundary mid-sequence
			typeInto("O")
			time.Sleep(30 * time.Millisecond)
			typeInto("A\r")
		}()
		r := read("> ")
		if r.s != "history line" {
			t.Fatalf("split SS3 sequence mishandled: %q", r.s)
		}
	})
}
