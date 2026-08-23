package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func callMsg(id, name, path string) Message {
	var c ToolCall
	c.ID = id
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = `{"path":"` + path + `"}`
	return Message{Role: "assistant", ToolCalls: []ToolCall{c}}
}

func TestPruneStaleReads(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		callMsg("r1", "read_file", "a.go"),
		{Role: "tool", ToolCallID: "r1", Content: strings.Repeat("old content of a.go ", 50)},
		callMsg("r2", "read_file", "b.go"),
		{Role: "tool", ToolCallID: "r2", Content: strings.Repeat("content of b.go ", 50)},
		callMsg("w1", "edit_file", "a.go"),
		{Role: "tool", ToolCallID: "w1", Content: "OK: replaced 1 occurrence(s)"},
		callMsg("r3", "read_file", "a.go"), // re-read AFTER the edit — must survive
		{Role: "tool", ToolCallID: "r3", Content: strings.Repeat("new content of a.go ", 50)},
	}
	pruned := pruneStaleReads(msgs)
	if pruned != 1 {
		t.Fatalf("want exactly 1 pruned, got %d", pruned)
	}
	if !strings.Contains(msgs[2].Content, "stale") {
		t.Fatal("pre-edit read of a.go must be marked stale")
	}
	if strings.Contains(msgs[4].Content, "stale") {
		t.Fatal("read of unmodified b.go must be untouched")
	}
	if strings.Contains(msgs[8].Content, "stale") {
		t.Fatal("post-edit re-read of a.go must be untouched")
	}
	if msgs[2].ToolCallID != "r1" {
		t.Fatal("tool_call_id must survive pruning (transcript validity)")
	}
	// Idempotent: a second pass prunes nothing new.
	if again := pruneStaleReads(msgs); again != 0 {
		t.Fatalf("second pass must be a no-op, pruned %d", again)
	}
}

func TestManageContextNoOpBelowBudget(t *testing.T) {
	// Below the budget, manageContext must not touch a single byte —
	// history edits invalidate the server's KV-cache prefix.
	msgs := []Message{
		callMsg("r1", "read_file", "a.go"),
		{Role: "tool", ToolCallID: "r1", Content: strings.Repeat("x", 5000)},
		callMsg("w1", "write_file", "a.go"),
		{Role: "tool", ToolCallID: "w1", Content: "OK"},
	}
	before := msgs[1].Content
	_ = manageContext(msgs)
	if msgs[1].Content != before {
		t.Fatal("manageContext must be a strict no-op below the token budget")
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
	os.MkdirAll(filepath.Join(dir, "sub", "node_modules"), 0o755)
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
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.Execute("spawn_task", map[string]any{"task": "do something"})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "subtask") {
		t.Fatalf("depth guard must reject nested spawns, got %q", out)
	}
}

func TestVerifyLoopSkipsAndPasses(t *testing.T) {
	sb := &Sandbox{Root: t.TempDir()}
	msgs := []Message{{Role: "system", Content: "s"}}

	// No verify command configured: untouched.
	verifyCommand = ""
	out := runVerifyLoop("", "", sb, &SessionStore{}, msgs, 0)
	if len(out) != 1 {
		t.Fatal("no verify command must be a no-op")
	}

	// Configured but nothing modified: untouched.
	verifyCommand = "false" // would fail if it ran
	defer func() { verifyCommand = "" }()
	out = runVerifyLoop("", "", sb, &SessionStore{}, msgs, len(sb.Modified))
	if len(out) != 1 {
		t.Fatal("no modifications must be a no-op")
	}

	// Modified + passing verify: runs, passes, appends nothing.
	verifyCommand = "true"
	sb.Modified = append(sb.Modified, "/tmp/x")
	out = runVerifyLoop("", "", sb, &SessionStore{}, msgs, 0)
	if len(out) != 1 {
		t.Fatal("passing verify must append no messages")
	}
}

func TestExecutePanicRecovery(t *testing.T) {
	registerTools(Tool{
		Name:    "panic_test",
		Desc:    "test-only.",
		Props:   map[string]any{},
		Handler: func(s *Sandbox, a toolArgs) string { panic("boom") },
	})
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.Execute("panic_test", map[string]any{})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "boom") {
		t.Fatalf("panic must convert to an ERROR result, got %q", out)
	}
}

// fakeMCPServer is a python one-liner speaking enough newline-delimited
// JSON-RPC to exercise initialize, tools/list, and tools/call.
const fakeMCPServer = `
import sys, json
for line in sys.stdin:
    req = json.loads(line)
    m, i = req.get("method"), req.get("id")
    if i is None: continue
    if m == "initialize":
        r = {"protocolVersion": "2024-11-05", "capabilities": {}, "serverInfo": {"name": "fake"}}
    elif m == "tools/list":
        r = {"tools": [{"name": "echo", "description": "Echoes text back.",
             "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]}
    elif m == "tools/call":
        r = {"content": [{"type": "text", "text": "echo: " + req["params"]["arguments"]["text"]}], "isError": False}
    else:
        r = {}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": i, "result": r}) + "\n")
    sys.stdout.flush()
`

func TestMCPClientAgainstFakeServer(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake_mcp.py")
	if err := os.WriteFile(script, []byte(fakeMCPServer), 0o644); err != nil {
		t.Fatal(err)
	}
	regBefore := len(registry)
	srv, n, err := startMCPServer("fake", MCPServerConfig{Command: "python3", Args: []string{script}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.stop()
	if n != 1 {
		t.Fatalf("want 1 tool registered, got %d", n)
	}
	if _, ok := toolByName["fake_echo"]; !ok {
		t.Fatal("tool must be registered as fake_echo")
	}
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.Execute("fake_echo", map[string]any{"text": "hello"})
	if out != "echo: hello" {
		t.Fatalf("tools/call round trip failed: %q", out)
	}
	// Cleanup registry entry so other tests aren't affected.
	registry = registry[:regBefore]
	delete(toolByName, "fake_echo")
	buildToolSchemas()
}

func feedLines(ls ...string) func() {
	old := lines
	lines = make(chan string, len(ls)+1)
	for _, l := range ls {
		lines <- l
	}
	return func() { lines = old }
}

func TestReadInputBracketedPaste(t *testing.T) {
	restore := feedLines(
		pasteStart+"PS /mnt/z> go run . list",
		"0  %!s(int=0) one two todo",
		"0  %!s(int=0) three   todo"+pasteEnd+" what is this?",
	)
	defer restore()
	got, ok := readInput()
	if !ok {
		t.Fatal("unexpected EOF")
	}
	want := "PS /mnt/z> go run . list\n0  %!s(int=0) one two todo\n0  %!s(int=0) three   todo what is this?"
	if got != want {
		t.Fatalf("paste not assembled:\n got %q\nwant %q", got, want)
	}
}

func TestReadInputSingleLinePaste(t *testing.T) {
	restore := feedLines(pasteStart + "just one line" + pasteEnd)
	defer restore()
	got, _ := readInput()
	if got != "just one line" {
		t.Fatalf("got %q", got)
	}
}

func TestReadInputLenientTripleQuoteOpener(t *testing.T) {
	restore := feedLines(`""" first pasted line`, "second line", `"""`)
	defer restore()
	got, _ := readInput()
	if got != "first pasted line\nsecond line" {
		t.Fatalf("lenient opener failed: %q", got)
	}
}

func TestReadInputPlainLineUnchanged(t *testing.T) {
	restore := feedLines("  hello there  ")
	defer restore()
	got, _ := readInput()
	if got != "hello there" {
		t.Fatalf("got %q", got)
	}
}
