package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffSingleTokenChange(t *testing.T) {
	// The motivating case: the old preview showed twelve lines for this;
	// a real diff shows the one changed pair plus context.
	oldT := "type Config struct {\n\tHost     string\n\tPort     String\n\tUser     string\n\tPassword string\n\tDBName   string\n}\n"
	newT := strings.Replace(oldT, "Port     String", "Port     string", 1)
	hunks := diffLines(oldT, newT)
	if len(hunks) != 1 {
		t.Fatalf("want 1 hunk, got %d", len(hunks))
	}
	minus, plus, ctx := 0, 0, 0
	for _, l := range hunks[0].lines {
		switch l.kind {
		case '-':
			minus++
		case '+':
			plus++
		case ' ':
			ctx++
		}
	}
	if minus != 1 || plus != 1 {
		t.Fatalf("single-line change must be 1 -/+ pair, got -%d +%d", minus, plus)
	}
	if ctx == 0 || ctx > 2*diffContext {
		t.Fatalf("context out of bounds: %d", ctx)
	}
	plain := renderDiff(hunks, false, 40)
	if !strings.Contains(plain, "- \tPort     String") || !strings.Contains(plain, "+ \tPort     string") {
		t.Fatalf("plain render wrong:\n%s", plain)
	}
	if !strings.Contains(plain, "@@ -") {
		t.Fatal("hunk header missing")
	}
}

func TestDiffIdentical(t *testing.T) {
	if diffLines("a\nb\n", "a\nb\n") != nil {
		t.Fatal("identical texts must produce no hunks")
	}
}

func TestDiffMultipleHunks(t *testing.T) {
	oldT := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\n"
	newT := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nK\nl\n" // two changes, far apart
	hunks := diffLines(oldT, newT)
	if len(hunks) != 2 {
		t.Fatalf("distant changes must split into hunks, got %d:\n%s", len(hunks), renderDiff(hunks, false, 40))
	}
	if hunks[1].oldStart <= hunks[0].oldStart {
		t.Fatal("hunk starts must be ordered")
	}
}

func TestDiffPureAddition(t *testing.T) {
	hunks := diffLines("a\nb\n", "a\nnew line\nb\n")
	plain := renderDiff(hunks, false, 40)
	if !strings.Contains(plain, "+ new line") || strings.Contains(plain, "- ") {
		t.Fatalf("pure insertion rendered wrong:\n%s", plain)
	}
}

func TestDiffNewFile(t *testing.T) {
	// /diff on a file created this session: old side empty.
	hunks := diffLines("", "line1\nline2\n")
	plain := renderDiff(hunks, false, 40)
	if !strings.Contains(plain, "+ line1") || !strings.Contains(plain, "+ line2") {
		t.Fatalf("new-file diff wrong:\n%s", plain)
	}
}

func TestDiffCapOverflow(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 60; i++ {
		oldB.WriteString("same\n")
		newB.WriteString("changed\n")
	}
	plain := renderDiff(diffLines(oldB.String(), newB.String()), false, 10)
	if !strings.Contains(plain, "more changed lines") {
		t.Fatalf("overflow note missing:\n%s", plain[:200])
	}
	if strings.Count(plain, "changed") > 25 { // 10 shown + note; generous bound
		t.Fatal("cap not enforced")
	}
}

func TestHighlightSpan(t *testing.T) {
	got := highlightSpan("\tPort     String", "\tPort     string")
	if !strings.Contains(got, "\033[7mS\033[27m") {
		t.Fatalf("changed char must be reverse-video: %q", got)
	}
	// Oversized lines pass through unstyled.
	long := strings.Repeat("x", 400)
	if highlightSpan(long, long+"y") != long {
		t.Fatal("oversized lines must not be styled")
	}
}

func TestEditResultCarriesDiff(t *testing.T) {
	dir := t.TempDir()
	oldHooks := hooks
	hooks = nil
	defer func() { hooks = oldHooks }()
	os.WriteFile(filepath.Join(dir, "c.go"), []byte("package x\n\nvar Port String\n"), 0o644)
	sb := &Sandbox{Root: dir}
	out := sb.Execute("edit_file", map[string]any{
		"path": "c.go", "old_str": "Port String", "new_str": "Port string",
	})
	if !strings.HasPrefix(out, "OK") {
		t.Fatalf("edit failed: %q", out)
	}
	if !strings.Contains(out, "Diff of the change:") || !strings.Contains(out, "+ var Port string") {
		t.Fatalf("tool result must carry the diff:\n%s", out)
	}
}

func TestOverwriteResultCarriesDiff(t *testing.T) {
	dir := t.TempDir()
	oldHooks := hooks
	hooks = nil
	defer func() { hooks = oldHooks }()
	os.WriteFile(filepath.Join(dir, "d.txt"), []byte("alpha\nbeta\n"), 0o644)
	sb := &Sandbox{Root: dir}
	out := sb.Execute("write_file", map[string]any{"path": "d.txt", "content": "alpha\ngamma\n"})
	if !strings.Contains(out, "Diff vs the previous content:") || !strings.Contains(out, "+ gamma") {
		t.Fatalf("overwrite result must carry the diff:\n%s", out)
	}
}
