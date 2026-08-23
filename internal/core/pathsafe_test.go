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
