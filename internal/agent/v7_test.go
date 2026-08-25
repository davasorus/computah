package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShrinkOldToolResultsBoundaries(t *testing.T) {
	// Contract of shrinkOldToolResults (see session.go):
	//   - keepRecent = 4: the 4 most recent tool results are ALWAYS kept intact.
	//   - threshold  = 2048: among OLDER results (before that window), only
	//     those longer than 2048 bytes are elided (head 1024 + marker + tail 256).
	//   - older results <= 2048 are left untouched.
	// So truncation only happens to a large tool result that is BOTH old
	// (outside the last 4) AND over the threshold.

	const threshold = 2048

	// helper: build an interleaved assistant/tool message slice from contents.
	build := func(contents []string) []Message {
		var msgs []Message
		for i, c := range contents {
			id := fmt.Sprintf("r%d", i+1)
			msgs = append(msgs,
				Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Type: "function"}}},
				Message{Role: "tool", ToolCallID: id, Content: c},
			)
		}
		return msgs
	}
	// contentAt returns the tool content for the nth tool result (0-indexed);
	// tool messages sit at odd indices 1,3,5,...
	contentAt := func(msgs []Message, n int) string { return msgs[2*n+1].Content }

	// --- Case 1: fewer than keepRecent tool results => nothing is elided,
	// even a large one, because it is still "recent". ---
	msgs1 := build([]string{strings.Repeat("a", 3000), "ok"})
	_ = shrinkOldToolResults(msgs1)
	if got := len(contentAt(msgs1, 0)); got != 3000 {
		t.Errorf("case1: only 2 tool results (both recent) — large one should be untouched, got %d", got)
	}

	// --- Case 2: 6 tool results. Last 4 are kept intact; the oldest 2 are
	// elible. Sizes chosen to exercise every branch. ---
	msgs2 := build([]string{
		strings.Repeat("a", 3000), // #0 old, large   -> elided
		strings.Repeat("a", 1000), // #1 old, small   -> untouched (<= threshold)
		strings.Repeat("a", 3000), // #2 recent, large -> untouched (within last 4)
		strings.Repeat("a", 2048), // #3 recent, at threshold -> untouched
		strings.Repeat("a", 3000), // #4 recent, large -> untouched
		strings.Repeat("a", 3000), // #5 recent, large -> untouched
	})
	_ = shrinkOldToolResults(msgs2)

	if got := len(contentAt(msgs2, 0)); got >= threshold {
		t.Errorf("#0 (old, large) should be elided below %d, got %d", threshold, got)
	}
	if got := len(contentAt(msgs2, 1)); got != 1000 {
		t.Errorf("#1 (old, small) should be untouched at 1000, got %d", got)
	}
	if got := len(contentAt(msgs2, 2)); got != 3000 {
		t.Errorf("#2 (recent, large) should be untouched at 3000, got %d", got)
	}
	if got := len(contentAt(msgs2, 3)); got != 2048 {
		t.Errorf("#3 (recent, at threshold) should be untouched at 2048, got %d", got)
	}
	if got := len(contentAt(msgs2, 4)); got != 3000 {
		t.Errorf("#4 (recent, large) should be untouched at 3000, got %d", got)
	}
	if got := len(contentAt(msgs2, 5)); got != 3000 {
		t.Errorf("#5 (recent, large) should be untouched at 3000, got %d", got)
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
	// toolSpawnTask must refuse to spawn a subtask when already inside one
	// (spawnDepth >= 1): subtasks spawning subtasks is disallowed. See loop.go.
	s := &Sandbox{Root: t.TempDir()}

	// Simulate being one level deep already.
	spawnDepth = 1
	defer func() { spawnDepth = 0 }()

	out := toolSpawnTask(s, toolArgs{"task": "do something"})
	if !strings.Contains(out, "cannot spawn further subtasks") {
		t.Errorf("expected refusal when spawnDepth>=1, got: %q", out)
	}

	// And an empty task is rejected regardless of depth.
	spawnDepth = 0
	if out := toolSpawnTask(s, toolArgs{"task": "  "}); !strings.Contains(out, "task must describe") {
		t.Errorf("expected empty-task rejection, got: %q", out)
	}
}
