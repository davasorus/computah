package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFilesAtomicSuccess(t *testing.T) {
	registerStructuredTools()
	dir := t.TempDir()
	oldHooks := hooks
	hooks = nil
	defer func() { hooks = oldHooks }()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc Old() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\nvar _ = Old\n"), 0o644)
	sb := &Sandbox{Root: dir}
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
	registerStructuredTools()
	dir := t.TempDir()
	oldHooks := hooks
	hooks = nil
	defer func() { hooks = oldHooks }()
	// Second edit's old_str won't match → NOTHING should apply, incl. the
	// valid first edit. This is the property the DI refactor needed.
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("alpha\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("beta\n"), 0o644)
	sb := &Sandbox{Root: dir}
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
	registerStructuredTools()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "server.pem"), []byte("KEY\n"), 0o644)
	sb := &Sandbox{Root: dir}
	out := sb.Execute("edit_files", map[string]any{"edits": []any{
		map[string]any{"path": "server.pem", "old_str": "KEY", "new_str": "HACKED"},
	}})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "protected") {
		t.Fatalf("protected path must block the whole set: %q", out)
	}
}

func TestParseTestFailures(t *testing.T) {
	out := `=== RUN   TestFoo
--- FAIL: TestFoo (0.00s)
    foo_test.go:42: expected 3, got 4
=== RUN   TestBar
--- PASS: TestBar (0.00s)
--- FAIL: TestBaz (0.01s)
    baz_test.go:10: nil pointer
FAIL
FAIL    example  0.02s`
	got := parseTestFailures(out)
	if !strings.Contains(got, "TestFoo") || !strings.Contains(got, "TestBaz") {
		t.Fatalf("must list failing tests: %s", got)
	}
	if !strings.Contains(got, "foo_test.go:42") || !strings.Contains(got, "expected 3, got 4") {
		t.Fatalf("must extract assertions with file:line: %s", got)
	}
	if strings.Contains(got, "TestBar") {
		t.Fatalf("passing tests must not appear: %s", got)
	}
}

func TestParseTestFailuresBuildError(t *testing.T) {
	out := "# github.com/x/y\n./db.go:16:11: undefined: String\nFAIL"
	got := parseTestFailures(out)
	if !strings.Contains(got, "Build errors") || !strings.Contains(got, "undefined: String") {
		t.Fatalf("build errors must surface: %s", got)
	}
}

func TestParseTestFailuresClean(t *testing.T) {
	if got := parseTestFailures("ok  example  0.5s\n"); got != "" {
		t.Fatalf("clean output must parse to empty, got %q", got)
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

func TestForkTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := newSessionStore(t.TempDir())
	msgs := []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "root work"}}
	st.Append(msgs)
	rootName := filepath.Base(st.Path())
	child, err := st.Fork(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.sessionParent(child); got != rootName {
		t.Fatalf("child's parent must be the root: got %q want %q", got, rootName)
	}
	if st.sessionParent(rootName) != "" {
		t.Fatal("root has no parent")
	}
}
