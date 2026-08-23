package md

import (
	"strings"
	"testing"

	"github.com/davasorus/computah/internal/core"
)

// ---------- markdown renderer ----------
func TestMDInlineStyles(t *testing.T) {
	old := core.UseColor
	core.UseColor = true
	defer func() { core.UseColor = old }()
	m := NewMDWriter()
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
	old := core.UseColor
	core.UseColor = true
	defer func() { core.UseColor = old }()
	m := NewMDWriter()
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
	old := core.UseColor
	core.UseColor = true
	defer func() { core.UseColor = old }()
	m := NewMDWriter()
	for _, tok := range []string{"he", "llo **wo", "rld**"} {
		m.Write(tok) // no newline yet: nothing printed, no panic
	}
	if m.buf.String() != "hello **world**" {
		t.Fatalf("buffer misassembled: %q", m.buf.String())
	}
}
