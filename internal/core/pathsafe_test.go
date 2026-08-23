package core

import (
	"path/filepath"
	"testing"
)

func TestConfinePath(t *testing.T) {
	base := t.TempDir()

	// Legitimate: relative path within base.
	got, err := ConfinePath(base, "sub/file.go")
	if err != nil {
		t.Fatalf("relative path within base rejected: %v", err)
	}
	if want := filepath.Join(base, "sub/file.go"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// Legitimate: base itself.
	if _, err := ConfinePath(base, "."); err != nil {
		t.Fatalf("base dir rejected: %v", err)
	}

	// Escape attempts must all be rejected.
	for _, bad := range []string{
		"../../etc/passwd",
		"sub/../../../../etc/passwd",
		"/etc/passwd",
	} {
		if _, err := ConfinePath(base, bad); err == nil {
			t.Fatalf("escape %q was NOT rejected", bad)
		}
	}

	// The ".../...//" trick defeats naive "../"-stripping, but resolve-then-
	// check treats "..." as an ordinary (contained) directory name, so this
	// stays inside base and is correctly allowed — no traversal occurs.
	if _, err := ConfinePath(base, ".../...//file"); err != nil {
		t.Fatalf("contained %q wrongly rejected: %v", ".../...//file", err)
	}

	// Empty path is an error.
	if _, err := ConfinePath(base, ""); err == nil {
		t.Fatal("empty path should error")
	}

	// Sibling-prefix trick: base "/x/user" must not admit "/x/user-evil".
	if _, err := ConfinePath("/x/user", "/x/user-evil/secret"); err == nil {
		t.Fatal("sibling-prefix path was NOT rejected")
	}
}

// TestConfinePathMissingDirs guards the regression where resolving symlinks
// on a not-yet-existing target failed if the parent (or several ancestors)
// didn't exist yet — which broke creating a new file in a new subdirectory,
// a completely normal agent operation. ConfinePath must resolve to the
// nearest existing ancestor and re-append the missing suffix in order.
func TestConfinePathMissingDirs(t *testing.T) {
	base := t.TempDir()

	// New file, one missing dir level.
	got, err := ConfinePath(base, "newdir/file.go")
	if err != nil {
		t.Fatalf("one missing dir rejected: %v", err)
	}
	if !hasSuffix(got, "newdir/file.go") {
		t.Errorf("one-level: got %q, want suffix newdir/file.go", got)
	}

	// New file, several missing dir levels created at once.
	got, err = ConfinePath(base, "a/b/c/deep.go")
	if err != nil {
		t.Fatalf("multi missing dirs rejected: %v", err)
	}
	if !hasSuffix(got, "a/b/c/deep.go") {
		t.Errorf("multi-level: got %q, want suffix a/b/c/deep.go", got)
	}

	// A missing-dir path that still escapes must STILL be rejected.
	if _, err := ConfinePath(base, "../evil/new.go"); err == nil {
		t.Error("missing-dir escape was not rejected")
	}
}

func hasSuffix(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}
