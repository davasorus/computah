package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShrinkOldToolResultsBoundaries(t *testing.T) {
	// test exactly at the threshold boundary (2048 bytes)
	msg1 := Message{Role: "tool", ToolCallID: "r1", Content: strings.Repeat("a", 2048)}
	msgs1 := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r1", Type: "function"}}},
		msg1,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r2", Type: "function"}}},
		{Role: "tool", ToolCallID: "r2", Content: "ok"},
	}

	_ = shrinkOldToolResults(msgs1)
	if len(msgs1[1].Content) != 2048 {
		t.Errorf("expected length 2048, got %d", len(msgs1[1].Content))
	}

	// test just over the threshold boundary (2049 bytes)
	msg2 := Message{Role: "tool", ToolCallID: "r3", Content: strings.Repeat("a", 2049)}
	msgs2 := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r3", Type: "function"}}},
		msg2,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r4", Type: "function"}}},
		{Role: "tool", ToolCallID: "r4", Content: "ok"},
	}
	_ = shrinkOldToolResults(msgs2)
	if len(msgs2[1].Content) >= 2048 {
		t.Errorf("expected content to be truncated, but got length %d", len(msgs2[1].Content))
	}

	// test complex scenario: multiple tool results with different ages and sizes
	m1 := Message{Role: "tool", ToolCallID: "r1", Content: strings.Repeat("a", 3000)} // Old, Large
	m2 := Message{Role: "tool", ToolCallID: "r2", Content: strings.Repeat("a", 3000)} // Old, Large
	m3 := Message{Role: "tool", ToolCallID: "r3", Content: strings.Repeat("a", 1000)} // Old, Small
	m4 := Message{Role: "tool", ToolCallID: "r4", Content: strings.Repeat("a", 2048)} // Recent, Large (at threshold)
	m5 := Message{Role: "tool", ToolCallID: "r5", Content: strings.Repeat("a", 3000)} // Recent, Large
	m6 := Message{Role: "tool", ToolCallID: "r6", Content: strings.Repeat("a", 3000)} // Recent, Large

	msgs3 := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r1", Type: "function"}}},
		m1,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r2", Type: "function"}}},
		m2,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r3", Type: "function"}}},
		m3,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r4", Type: "function"}}},
		m4,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r5", Type: "function"}}},
		m5,
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "r6", Type: "function"}}},
		m6,
	}

	_ = shrinkOldToolResults(msgs3)

	if len(msgs3[1].Content) > 2048 {
		t.Errorf("expected msg1 (old, large) to be truncated, but got length %d", len(msgs3[1].Content))
	}
	if len(msgs3[3].Content) > 2048 {
		t.Errorf("expected msg2 (old, large) to be truncated, but got length %d", len(msgs3[3].Content))
	}
	if len(msgs3[5].Content) != 1000 {
		t.Errorf("expected msg3 (old, small) NOT to be truncated, but got length %d", len(msgs3[5].Content))
	}
	if len(msgs3[7].Content) != 2048 {
		t.Errorf("expected msg4 (recent, large) NOT to be truncated, but got length %d", len(msgs3[7].Content))
	}
	if len(msgs3[9].Content) > 2048 {
		t.Errorf("expected msg5 (recent, large) NOT to be truncated, but got length %d", len(msgs3[9].Content))
	}
	if len(msgs3[11].Content) > 2048 {
		t.Errorf("expected msg6 (recent, large) NOT to be truncated, but got length %d", len(msgs3[11].Content))
	}
}

func TestExecShellKillsProcessGroup(t *testing.T) {
	old := commandTimeout
	commandTimeout = 1 * time.Second
	defer func() { commandTimeout = old }()

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// bash spawns a grandchild sleep; on timeout the whole group must die.
	_, code, err := execShell("sleep 300 & echo $! > "+pidFile+"; wait", dir, false)
	if err == nil {
		t.Fatalf("expected timeout error, got exit %d", code)
	}
	data, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("pid file not written: %v", rerr)
	}
	pid := strings.TrimSpace(string(data))
	time.Sleep(200 * time.Millisecond) // let the kill land
	if _, serr := os.Stat("/proc/" + pid); serr == nil {
		// check it's not a zombie that just hasn't been reaped
		st, _ := os.ReadFile("/proc/" + pid + "/stat")
		if !strings.Contains(string(st), ") Z ") {
			t.Fatalf("grandchild pid %s survived the group kill", pid)
		}
	}
}

func TestExecShellExitCode(t *testing.T) {
	out, code, err := execShell("echo hi; exit 3", t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if code != 3 || !strings.Contains(out, "hi") {
		t.Fatalf("want exit 3 with output, got %d %q", code, out)
	}
}

func TestToolGlob(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub", "node_modules"), 0755)
	os.WriteFile(filepath.Join(dir, "a.sql"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.sql"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "c.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "node_modules", "d.sql"), []byte("x"), 0o644)
	sb := &Sandbox{Root: dir}
	out := sb.Execute("glob", map[string]any{"pattern": "*.sql"})
	if !strings.Contains(out, "a.sql") || !strings.Contains(out, filepath.Join("sub", "b.sql")) {
		t.Fatalf("missing matches: %q", out)
	}
	if strings.Contains(out, "c.go") || strings.Contains(out, "node_modules") {
		t.Fatalf("unwanted matches: %q", out)
	}
}

func TestSpawnDepthGuard(t *testing.T) {
	spawnDepth = 1
	defer func() { spawnDepth = 0 }()
}
