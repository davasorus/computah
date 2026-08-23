package agent

import (
	"path/filepath"
	"testing"
)

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
