package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSession(t *testing.T, msgs []Message) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	return f.Name()
}

func tc(id string) ToolCall {
	var c ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = "read_file"
	c.Function.Arguments = `{"path":"x"}`
	return c
}

func TestLoadSessionTrimsIncompleteTrailingGroup(t *testing.T) {
	// assistant makes 2 calls, crash after 1 result persisted
	path := writeSession(t, []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok"},
		{Role: "assistant", ToolCalls: []ToolCall{tc("a"), tc("b")}},
		{Role: "tool", ToolCallID: "a", Content: "r1"},
	})
	msgs, err := loadSession(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 0 {
		t.Fatalf("want the partial group dropped, got %+v", msgs)
	}
}

func TestLoadSessionTrimsBareTrailingAssistantCall(t *testing.T) {
	// crash before any result was written
	path := writeSession(t, []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCall{tc("a")}},
	})
	msgs, err := loadSession(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("want bare assistant tool_calls dropped, got %+v", msgs)
	}
}

func TestLoadSessionKeepsCompleteGroup(t *testing.T) {
	path := writeSession(t, []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []ToolCall{tc("a"), tc("b")}},
		{Role: "tool", ToolCallID: "a", Content: "r1"},
		{Role: "tool", ToolCallID: "b", Content: "r2"},
	})
	msgs, err := loadSession(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("complete group must survive, got %d msgs: %+v", len(msgs), msgs)
	}
}

func TestLoadSessionStillTrimsLeadingOrphans(t *testing.T) {
	// the tail window cuts mid-group: leading tool results have no carrier
	path := writeSession(t, []Message{
		{Role: "tool", ToolCallID: "a", Content: "r1"},
		{Role: "tool", ToolCallID: "b", Content: "r2"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok"},
	})
	msgs, err := loadSession(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" {
		t.Fatalf("want leading orphans trimmed, got %+v", msgs)
	}
}

func TestBackupOncePerSession(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := &Sandbox{Root: dir}

	r := sb.Execute("write_file", map[string]any{"path": "f.txt", "content": "first write"})
	if !strings.HasPrefix(r, "OK") {
		t.Fatalf("write 1 failed: %s", r)
	}
	r = sb.Execute("write_file", map[string]any{"path": "f.txt", "content": "second write"})
	if !strings.HasPrefix(r, "OK") {
		t.Fatalf("write 2 failed: %s", r)
	}
	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(bak) != "ORIGINAL" {
		t.Fatalf(".bak must hold pre-session content, got %q", bak)
	}
	cur, _ := os.ReadFile(target)
	if string(cur) != "second write" {
		t.Fatalf("target must hold latest write, got %q", cur)
	}
}

func TestBackupAlsoGuardsEditAfterWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "g.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	sb := &Sandbox{Root: dir}
	sb.Execute("write_file", map[string]any{"path": "g.txt", "content": "bad content"})
	sb.Execute("edit_file", map[string]any{"path": "g.txt", "old_str": "bad", "new_str": "worse"})
	bak, _ := os.ReadFile(target + ".bak")
	if string(bak) != "ORIGINAL" {
		t.Fatalf(".bak must survive a write+edit sequence, got %q", bak)
	}
}

func TestShrinkOldToolResults(t *testing.T) {
	big := strings.Repeat("x", 10_000)
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "tool", ToolCallID: "1", Content: big}, // old — should shrink
		{Role: "tool", ToolCallID: "2", Content: big}, // recent 4 — intact
		{Role: "tool", ToolCallID: "3", Content: big},
		{Role: "tool", ToolCallID: "4", Content: big},
		{Role: "tool", ToolCallID: "5", Content: big},
	}
	out := shrinkOldToolResults(msgs)
	if len(out[1].Content) >= 10_000 {
		t.Fatalf("old tool result not elided (len %d)", len(out[1].Content))
	}
	if !strings.Contains(out[1].Content, "elided") {
		t.Fatal("elision marker missing")
	}
	if !strings.HasPrefix(out[1].Content, "xxxx") || !strings.HasSuffix(out[1].Content, "xxxx") {
		t.Fatal("head/tail of elided content must be preserved")
	}
	for i := 2; i <= 5; i++ {
		if len(out[i].Content) != 10_000 {
			t.Fatalf("recent tool result %d was shrunk", i)
		}
	}
	if out[0].Content != "sys" {
		t.Fatal("non-tool message touched")
	}
}

func TestShrinkLeavesSmallResultsAlone(t *testing.T) {
	msgs := []Message{
		{Role: "tool", ToolCallID: "1", Content: "small"},
		{Role: "tool", ToolCallID: "2", Content: strings.Repeat("y", 3000)},
		{Role: "tool", ToolCallID: "3", Content: "a"},
		{Role: "tool", ToolCallID: "4", Content: "b"},
		{Role: "tool", ToolCallID: "5", Content: "c"},
		{Role: "tool", ToolCallID: "6", Content: "d"},
	}
	out := shrinkOldToolResults(msgs)
	if out[0].Content != "small" {
		t.Fatal("under-threshold result must not be touched")
	}
	if len(out[1].Content) >= 3000 {
		t.Fatal("over-threshold old result must be elided")
	}
}

func TestAutoApprovedCode(t *testing.T) {
	// Isolate from the real user's ~/.agent/allow.txt: autoApproved consults
	// userAllowPrefixes(), and without this the test's result depends on
	// whatever prefixes a real session has accumulated on this machine.
	t.Setenv("HOME", t.TempDir())
	cases := map[string]bool{
		"code":                        true,  // exact
		"code .":                      true,  // open workdir
		"code main.go":                true,  // open file
		"code --install-extension ms": false, // flags prompt
		"code -w file":                false,
		"git status":                  true,
		"git stash list":              true, // via prefix, exact dup removed
		"gh run view 12345":           true,
		"rm -rf /":                    false,
		"git status && rm -rf /":      false,
		"ls && pwd":                   true,
		"cat foo > bar":               false, // redirection
	}
	for cmd, want := range cases {
		if got := autoApproved(cmd); got != want {
			t.Errorf("autoApproved(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// Verify the persisted counter means an in-place shrink of already-persisted
// messages never rewrites them: disk keeps full fidelity.
func TestAppendThenShrinkKeepsDiskFullFidelity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	st := newSessionStore(dir)
	big := strings.Repeat("z", 10_000)
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "tool", ToolCallID: "1", Content: big},
		{Role: "tool", ToolCallID: "2", Content: big},
		{Role: "tool", ToolCallID: "3", Content: big},
		{Role: "tool", ToolCallID: "4", Content: big},
		{Role: "tool", ToolCallID: "5", Content: big},
	}
	st.Append(msgs)                   // persist full content first (runTurn loop-top order)
	msgs = shrinkOldToolResults(msgs) // then shrink in memory
	if len(msgs[1].Content) >= 10_000 {
		t.Fatal("in-memory copy should be elided")
	}
	data, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "elided") {
		t.Fatal("disk must never contain elided content")
	}
	if strings.Count(string(data), big) != 5 {
		t.Fatal("disk must hold all five full tool results")
	}
}
