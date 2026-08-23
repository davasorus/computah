package toolsext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davasorus/computah/internal/agent"
)

func TestEditFilesAtomicSuccess(t *testing.T) {
	RegisterStructuredTools()
	dir := t.TempDir()
	oldHooks := agent.Hooks()
	agent.SetHooks(nil)
	defer agent.SetHooks(oldHooks)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc Old() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\nvar _ = Old\n"), 0o644)
	sb := &agent.Sandbox{Root: dir}
	out := sb.Execute("edit_files", map[string]any{"edits": []any{
		map[string]any{"path": "a.go", "old_str": "func Old()", "new_str": "func New()"},
		map[string]any{"path": "b.go", "old_str": "= Old", "new_str": "= New"},
	}})
	if !strings.HasPrefix(out, "OK") {
		t.Fatalf("atomic edit failed: %q", out)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(a), "func New()") || !strings.Contains(string(b), "= New") {
		t.Fatal("both files must reflect the change")
	}
}

func TestEditFilesAtomicRollback(t *testing.T) {
	RegisterStructuredTools()
	dir := t.TempDir()
	oldHooks := agent.Hooks()
	agent.SetHooks(nil)
	defer agent.SetHooks(oldHooks)
	// Second edit's old_str won't match → NOTHING should apply, incl. the
	// valid first edit. This is the property the DI refactor needed.
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("alpha\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("beta\n"), 0o644)
	sb := &agent.Sandbox{Root: dir}
	out := sb.Execute("edit_files", map[string]any{"edits": []any{
		map[string]any{"path": "a.go", "old_str": "alpha", "new_str": "ALPHA"},
		map[string]any{"path": "b.go", "old_str": "nonexistent", "new_str": "X"},
	}})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "nothing applied") {
		t.Fatalf("must refuse atomically: %q", out)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if string(a) != "alpha\n" {
		t.Fatalf("valid edit must NOT have applied when a later edit failed: %q", a)
	}
	if len(sb.Modified) != 0 {
		t.Fatal("no files should be marked modified on rollback")
	}
}

func TestEditFilesProtected(t *testing.T) {
	RegisterStructuredTools()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "server.pem"), []byte("KEY\n"), 0o644)
	sb := &agent.Sandbox{Root: dir}
	out := sb.Execute("edit_files", map[string]any{"edits": []any{
		map[string]any{"path": "server.pem", "old_str": "KEY", "new_str": "HACKED"},
	}})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "protected") {
		t.Fatalf("protected path must block the whole set: %q", out)
	}
}

func TestFindIdentOffset(t *testing.T) {
	src := "var xConfig int\nConfig := 1\nfoo.Config = 2\n"
	// Must skip xConfig (substring) and hit standalone Config on line 2.
	off := findIdentOffset(src, "Config")
	if off < 0 || src[off:off+6] != "Config" {
		t.Fatalf("offset %d wrong", off)
	}
	if off <= strings.Index(src, "xConfig") {
		t.Fatal("must not match inside xConfig")
	}
	if findIdentOffset("no match here", "Zzz") != -1 {
		t.Fatal("absent ident must be -1")
	}
}
