package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := gitRun(dir, args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("original\n"), 0o644)
	if out, err := gitRun(dir, "add", "-A"); err != nil {
		t.Fatal(out)
	}
	if out, err := gitRun(dir, "commit", "-qm", "chore: init"); err != nil {
		t.Fatal(out)
	}
	return dir
}

func TestCheckpointAndRewind(t *testing.T) {
	dir := initGitRepo(t)
	oldCPs, oldOff := checkpoints, checkpointsOff
	checkpoints, checkpointsOff = nil, false
	defer func() { checkpoints, checkpointsOff = oldCPs, oldOff }()

	// Turn 1: user has uncommitted work; checkpoint must capture it.
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("user edit\n"), 0o644)
	takeCheckpoint(dir, "first prompt")
	if len(checkpoints) != 1 || checkpoints[0].hash == "" {
		t.Fatalf("dirty tree must produce a hash checkpoint: %+v", checkpoints)
	}
	// Model wrecks the file.
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("model wreckage\n"), 0o644)

	// Rewind to checkpoint 1 (drive the same git ops handleRewind uses).
	c := checkpoints[0]
	if out, err := gitRun(dir, "checkout", c.hash, "--", "."); err != nil {
		t.Fatalf("rewind: %v %s", err, out)
	}
	gitRun(dir, "reset", "-q")
	data, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if string(data) != "user edit\n" {
		t.Fatalf("rewind must restore the pre-turn state (incl. uncommitted work), got %q", data)
	}
}

func TestCheckpointCleanTree(t *testing.T) {
	dir := initGitRepo(t)
	oldCPs, oldOff := checkpoints, checkpointsOff
	checkpoints, checkpointsOff = nil, false
	defer func() { checkpoints, checkpointsOff = oldCPs, oldOff }()

	takeCheckpoint(dir, "clean start")
	if len(checkpoints) != 1 || checkpoints[0].hash != "" {
		t.Fatalf("clean tree must checkpoint with empty hash: %+v", checkpoints)
	}
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("wreckage\n"), 0o644)
	if out, err := gitRun(dir, "checkout", "HEAD", "--", "."); err != nil {
		t.Fatal(out)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.go"))
	if string(data) != "original\n" {
		t.Fatalf("clean-checkpoint rewind must restore HEAD, got %q", data)
	}
}

func TestCheckpointSkipsNonRepo(t *testing.T) {
	oldCPs := checkpoints
	checkpoints = nil
	defer func() { checkpoints = oldCPs }()
	takeCheckpoint(t.TempDir(), "x")
	if len(checkpoints) != 0 {
		t.Fatal("non-repo must not checkpoint")
	}
}

func TestHooksPostEditAndPreCommand(t *testing.T) {
	dir := t.TempDir()
	oldHooks, oldRoot := hooks, curRoot
	curRoot = dir
	marker := filepath.Join(dir, "hook-ran")
	hooks = map[string]string{
		"post_edit":   "touch " + marker + " && test -f {file}",
		"pre_command": "false", // block everything
	}
	defer func() { hooks, curRoot = oldHooks, oldRoot }()

	sb := &Sandbox{Root: dir}
	out := sb.Execute("write_file", map[string]any{"path": "a.txt", "content": "hello"})
	if !strings.HasPrefix(out, "OK") {
		t.Fatalf("write failed: %q", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("post_edit hook did not run")
	}

	oldYes := assumeYes
	assumeYes = true
	defer func() { assumeYes = oldYes }()
	out = sb.Execute("run_command", map[string]any{"command": "echo should-not-run"})
	if !strings.Contains(out, "pre_command") || strings.Contains(out, "should-not-run\n") {
		t.Fatalf("failing pre_command hook must block execution: %q", out)
	}
}

func TestHookFailureWarnsInResult(t *testing.T) {
	dir := t.TempDir()
	oldHooks, oldRoot := hooks, curRoot
	curRoot = dir
	hooks = map[string]string{"post_edit": "echo broken && false"}
	defer func() { hooks, curRoot = oldHooks, oldRoot }()
	sb := &Sandbox{Root: dir}
	out := sb.Execute("write_file", map[string]any{"path": "b.txt", "content": "x"})
	if !strings.HasPrefix(out, "OK") || !strings.Contains(out, "post_edit hook failed") {
		t.Fatalf("hook failure must warn but not fail the write: %q", out)
	}
}

func TestPlanModeBlocksMutations(t *testing.T) {
	dir := t.TempDir()
	planMode = true
	defer func() { planMode = false }()
	sb := &Sandbox{Root: dir}
	out := sb.Execute("write_file", map[string]any{"path": "x.txt", "content": "nope"})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "plan mode") {
		t.Fatalf("plan mode must block writes: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("file must not exist")
	}
	if !strings.HasPrefix(sb.Execute("list_dir", map[string]any{"path": "."}), "") {
		t.Fatal("unreachable")
	}
	// Read-only tools still work.
	if out := sb.Execute("tree", map[string]any{"path": "."}); strings.HasPrefix(out, "ERROR: plan mode") {
		t.Fatalf("read-only tool must work in plan mode: %q", out)
	}
}

func TestCurrentToolsFiltersInPlanMode(t *testing.T) {
	planMode = true
	defer func() { planMode = false }()
	for _, tl := range currentTools() {
		fn := tl["function"].(map[string]any)
		name := fn["name"].(string)
		if !readOnlyTools[name] {
			t.Fatalf("plan mode advertised mutating tool %q", name)
		}
	}
	planMode = false
	if len(currentTools()) != len(tools) {
		t.Fatal("normal mode must advertise everything")
	}
}

func TestStatsRecord(t *testing.T) {
	var s statsRecorder
	s.record(10000, 500, 1200, 2*time.Second, 12*time.Second)
	s.record(20000, 250, 800, 4*time.Second, 9*time.Second)
	if s.requests != 2 || s.promptTk != 30000 || s.genTk != 750 || s.thinkTk != 2000 {
		t.Fatalf("totals wrong: req=%d prompt=%d gen=%d", s.requests, s.promptTk, s.genTk)
	}
	if s.lastPrompt != 20000 || s.lastTTFB != 4*time.Second || s.lastGenDur != 5*time.Second {
		t.Fatalf("last-request tracking wrong: prompt=%d ttfb=%v gen=%v", s.lastPrompt, s.lastTTFB, s.lastGenDur)
	}
	// Degenerate: no bytes ever arrived (ttfb == total) must not go negative.
	s.record(100, 0, 0, 3*time.Second, 3*time.Second)
	if s.lastGenDur != 0 {
		t.Fatalf("gen duration must clamp at 0, got %v", s.lastGenDur)
	}
}

func TestCustomCommands(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".agent", "commands"), 0o755)
	os.MkdirAll(filepath.Join(root, ".agent", "commands"), 0o755)
	os.WriteFile(filepath.Join(home, ".agent", "commands", "migrate.md"),
		[]byte("PERSONAL: migrate $ARGUMENTS"), 0o644)
	os.WriteFile(filepath.Join(root, ".agent", "commands", "migrate.md"),
		[]byte("Migrate $ARGUMENTS to the reusable workflow. Verify with a dry run."), 0o644)
	os.WriteFile(filepath.Join(root, ".agent", "commands", "standup.md"),
		[]byte("Summarize the git log since yesterday."), 0o644)
	os.WriteFile(filepath.Join(root, ".agent", "commands", "verify.md"),
		[]byte("should be ignored"), 0o644)

	oldCmds := customCommands
	oldRepl := append([]string(nil), replCommands...)
	customCommands = map[string]string{}
	defer func() { customCommands = oldCmds; replCommands = oldRepl }()

	loadCustomCommands(root)
	if len(customCommands) != 2 {
		t.Fatalf("want 2 commands (verify shadow ignored), got %v", customCommands)
	}
	// Repo-local shadows personal.
	got, ok := expandCustomCommand("/migrate proj5")
	if !ok || !strings.Contains(got, "reusable workflow") || !strings.Contains(got, "proj5") {
		t.Fatalf("expansion wrong: %q ok=%v", got, ok)
	}
	if strings.Contains(got, "PERSONAL") {
		t.Fatal("repo-local must shadow personal")
	}
	// $ARGUMENTS required but missing → not expanded.
	if _, ok := expandCustomCommand("/migrate"); ok {
		t.Fatal("missing required arguments must not expand")
	}
	// No-argument template works bare.
	if got, ok := expandCustomCommand("/standup"); !ok || !strings.Contains(got, "git log") {
		t.Fatalf("bare template: %q ok=%v", got, ok)
	}
	// Non-command input untouched.
	if _, ok := expandCustomCommand("fix the bug"); ok {
		t.Fatal("plain input must not expand")
	}
}

func TestSessionTitles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "20260814-101010.jsonl")
	os.WriteFile(p, []byte("{}\n"), 0o644)
	if sessionTitle(p) != "" {
		t.Fatal("no sidecar yet")
	}
	os.WriteFile(titlePath(p), []byte("fix proj9 db config\n"), 0o644)
	if sessionTitle(p) != "fix proj9 db config" {
		t.Fatalf("got %q", sessionTitle(p))
	}
	if titlePath(p) != filepath.Join(dir, "20260814-101010.title") {
		t.Fatalf("sidecar path wrong: %s", titlePath(p))
	}
}

func TestAgentRoleParsing(t *testing.T) {
	r := parseAgentRole("reviewer", "model: qwen2.5-coder-14b\n\nYou are a strict reviewer. Report, don't fix.")
	if r.Model != "qwen2.5-coder-14b" || !strings.HasPrefix(r.Prompt, "You are a strict reviewer") {
		t.Fatalf("parse wrong: %+v", r)
	}
	// No header: whole body is the prompt.
	r = parseAgentRole("docs", "Write ASD-STE100 documentation only.")
	if r.Model != "" || r.Prompt != "Write ASD-STE100 documentation only." {
		t.Fatalf("headerless parse wrong: %+v", r)
	}
	// Empty body → empty prompt (loader skips these).
	if parseAgentRole("x", "model: y\n\n").Prompt != "" {
		t.Fatal("empty prompt must stay empty")
	}
}

func TestAgentRolesLoadAndShadow(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".agent", "agents"), 0o755)
	os.MkdirAll(filepath.Join(root, ".agent", "agents"), 0o755)
	os.WriteFile(filepath.Join(home, ".agent", "agents", "reviewer.md"), []byte("personal reviewer"), 0o644)
	os.WriteFile(filepath.Join(root, ".agent", "agents", "reviewer.md"), []byte("repo reviewer"), 0o644)
	old := agentRoles
	agentRoles = map[string]agentRole{}
	defer func() { agentRoles = old }()
	loadAgentRoles(root)
	if agentRoles["reviewer"].Prompt != "repo reviewer" {
		t.Fatalf("repo must shadow personal: %+v", agentRoles["reviewer"])
	}
}

func TestSpawnUnknownRoleErrors(t *testing.T) {
	old := agentRoles
	agentRoles = map[string]agentRole{"reviewer": {Name: "reviewer", Prompt: "x"}}
	defer func() { agentRoles = old }()
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.Execute("spawn_task", map[string]any{"task": "review it", "role": "reviwer"})
	if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "reviewer") {
		t.Fatalf("unknown role must error and list available: %q", out)
	}
}

func TestUpdateTodos(t *testing.T) {
	oldTodos := todos
	defer func() { todos = oldTodos }()
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.Execute("update_todos", map[string]any{"todos": []any{
		map[string]any{"text": "read the files", "done": true},
		map[string]any{"text": "fix the bug"},
	}})
	if !strings.Contains(out, "1/2 done") {
		t.Fatalf("got %q", out)
	}
	if len(todos) != 2 || !todos[0].Done || todos[1].Done {
		t.Fatalf("state wrong: %+v", todos)
	}
	// Works in plan mode (planning is its job).
	planMode = true
	defer func() { planMode = false }()
	out = sb.Execute("update_todos", map[string]any{"todos": []any{map[string]any{"text": "step"}}})
	if strings.HasPrefix(out, "ERROR") {
		t.Fatalf("must be allowed in plan mode: %q", out)
	}
	// Bad payload errors cleanly.
	if out := sb.Execute("update_todos", map[string]any{"todos": "not an array"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatalf("bad payload must error: %q", out)
	}
}

func TestAuxModelFallback(t *testing.T) {
	oldAux, oldCur := auxModel, curModel
	defer func() { auxModel, curModel = oldAux, oldCur }()
	curModel = "big"
	auxModel = ""
	if auxModelFor() != "big" {
		t.Fatal("empty aux must fall back to main")
	}
	auxModel = "small"
	if auxModelFor() != "small" {
		t.Fatal("aux must win when set")
	}
}

func TestNamedSessionStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := newSessionStore(t.TempDir())
	st.name = "eval-printf-run1-120000"
	st.Append([]Message{{Role: "user", Content: "x"}})
	if st.file == nil || !strings.HasSuffix(st.Path(), "eval-printf-run1-120000.jsonl") {
		t.Fatalf("named store wrong path: %q", st.Path())
	}
}

func TestSpinnerLabelUpdate(t *testing.T) {
	s := &spinner{stop: make(chan struct{}), done: make(chan struct{}), label: "a"}
	s.SetLabel("writing write_file call (~500 tokens)")
	s.mu.Lock()
	got := s.label
	s.mu.Unlock()
	if !strings.Contains(got, "write_file") {
		t.Fatalf("label not updated: %q", got)
	}
	close(s.done) // never started; satisfy Stop invariants if called
}

func TestMaxTokensInRequest(t *testing.T) {
	old := maxGenTokens
	maxGenTokens = 4096
	defer func() { maxGenTokens = old }()
	req := ChatRequest{Model: "m", Stream: true, MaxTokens: maxGenTokens}
	data, _ := json.Marshal(req)
	if !strings.Contains(string(data), `"max_tokens":4096`) {
		t.Fatalf("cap missing from payload: %s", data)
	}
}

func TestParseSSEReasoning(t *testing.T) {
	// Replica of the captured LM Studio stream shape: reasoning deltas,
	// then content, then DONE.
	stream := `data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"Let me think about "},"finish_reason":null}]}

data: {"choices":[{"delta":{"reasoning_content":"the verb mismatch here."},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":"Use %d for ints."},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	var got []string
	msg, finish, think, err := parseSSE(strings.NewReader(stream), func(tok string) { got = append(got, tok) })
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Use %d for ints." || finish != "stop" {
		t.Fatalf("content/finish wrong: %q %q", msg.Content, finish)
	}
	if think.chars != len("Let me think about ")+len("the verb mismatch here.") {
		t.Fatalf("reasoning not counted: %d", think.chars)
	}
	if strings.Contains(msg.Content, "think") {
		t.Fatal("reasoning must NOT leak into content when content exists")
	}
	if len(got) != 1 {
		t.Fatalf("onToken must fire only for content: %v", got)
	}
}

func TestParseSSEAnswerOnlyInReasoning(t *testing.T) {
	// The observed failure: minutes of thinking, then an empty reply.
	stream := `data: {"choices":[{"delta":{"reasoning_content":"` + strings.Repeat("reason ", 100) + `The answer is to rename OpenDB."},"finish_reason":null}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]
`
	msg, _, think, err := parseSSE(strings.NewReader(stream), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "" {
		t.Fatalf("parseSSE itself must not fabricate content: %q", msg.Content)
	}
	if think.chars == 0 || !strings.HasSuffix(think.tail, "rename OpenDB.") {
		t.Fatalf("tail must keep the end of the thinking: %q", think.tail[max(0, len(think.tail)-40):])
	}
	if len(think.tail) > 400 {
		t.Fatalf("tail must be capped: %d", len(think.tail))
	}
}

func TestEvalTranscriptsExcludedFromResume(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := newSessionStore(t.TempDir())
	// A real conversation and an eval transcript side by side in the root
	// (the pre-subdirectory layout that caused the ghost-hunt sessions).
	os.WriteFile(filepath.Join(st.dir, "20260814-120000.jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(st.dir, "eval-type-typo-run1-151104.jsonl"), []byte("{}\n"), 0o644)
	names := st.sessionNames()
	if len(names) != 1 || names[0] != "20260814-120000.jsonl" {
		t.Fatalf("eval transcripts must be invisible to resume: %v", names)
	}
	latest, err := st.latestSession()
	if err != nil || !strings.HasSuffix(latest, "20260814-120000.jsonl") {
		t.Fatalf("latest must skip eval files: %q %v", latest, err)
	}
}

func TestIsIdempotentCall(t *testing.T) {
	// Hermetic HOME with a broad user allowlist entry — the real-machine
	// configuration that broke this test's first version: "go " allowlisted
	// makes "go run . migrate" auto-APPROVED, but it must never be
	// auto-IDEMPOTENT.
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".agent"), 0o755)
	os.WriteFile(filepath.Join(home, ".agent", "allow.txt"), []byte("go \n"), 0o644)
	if !autoApproved("go run . migrate") {
		t.Fatal("user prefix must still auto-approve (prompting convenience)")
	}
	mk := func(name, args string) ToolCall {
		var c ToolCall
		c.Function.Name = name
		c.Function.Arguments = args
		return c
	}
	cases := []struct {
		tc   ToolCall
		want bool
	}{
		{mk("read_file", `{"path":"x"}`), true},
		{mk("search_files", `{"pattern":"String"}`), true},
		{mk("run_command", `{"command":"grep -n String Database/db.go"}`), true}, // allowlisted read
		{mk("run_command", `{"command":"go run . migrate"}`), false},             // mutating: repeat may be intentional
		{mk("edit_file", `{"path":"x","old_str":"a","new_str":"b"}`), false},     // mutation
		{mk("run_command", `{not json`), false},
	}
	for _, c := range cases {
		if got := isIdempotentCall(c.tc); got != c.want {
			t.Errorf("isIdempotentCall(%s %s) = %v, want %v", c.tc.Function.Name, c.tc.Function.Arguments, got, c.want)
		}
	}
}

func TestSigCountWindow(t *testing.T) {
	w := []string{"a", "b", "a", "c", "a"}
	if sigCount(w, "a") != 3 || sigCount(w, "b") != 1 || sigCount(w, "z") != 0 {
		t.Fatalf("counts wrong: a=%d b=%d z=%d", sigCount(w, "a"), sigCount(w, "b"), sigCount(w, "z"))
	}
	// The runTurn rule: a call is refused when it already appeared TWICE
	// among the PRIOR window entries (third occurrence overall) — A-B-A-B
	// cycles trip it, a single revisit does not.
	prior := []string{"searchX", "readA", "searchX", "readA"}
	if sigCount(prior, "searchX") < 2 {
		t.Fatal("A-B-A-B cycle must reach the refusal threshold")
	}
	if sigCount([]string{"searchX", "readA"}, "searchX") >= 2 {
		t.Fatal("a single revisit must NOT trip the threshold")
	}
}

func TestInterruptSalvagesReasoningTail(t *testing.T) {
	// The observed failure: user interrupts mid-dither, and the model's
	// just-reached conclusion lived in the reasoning stream. The cancel
	// path must surface it, same as the empty-reply path does.
	stream := `data: {"choices":[{"delta":{"reasoning_content":"Wait, actually... I will update both todo.go and cmd in this turn."},"finish_reason":null}]}
`
	// No [DONE]: simulates a stream cut off by cancellation mid-read.
	msg, _, think, _ := parseSSE(strings.NewReader(stream), func(string) {})
	if msg.Content != "" || think.chars == 0 {
		t.Fatalf("precondition: content empty, reasoning captured (got %q, %d)", msg.Content, think.chars)
	}
	if !strings.HasSuffix(think.tail, "in this turn.") {
		t.Fatalf("tail must hold the conclusion: %q", think.tail)
	}
}

func TestProtectedPaths(t *testing.T) {
	dir := t.TempDir()
	oldPat := protectedPatterns
	protectedPatterns = append(append([]string{}, oldPat...), ".env", "secrets/*")
	defer func() { protectedPatterns = oldPat }()
	sb := &Sandbox{Root: dir}
	for _, p := range []string{"server.pem", "deploy.key", "id_rsa.pub", ".env", "secrets/prod.json", "sub/dir/ca.pem"} {
		out := sb.Execute("write_file", map[string]any{"path": p, "content": "x"})
		if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "protected") {
			t.Fatalf("%s must be write-protected: %q", p, out)
		}
		out = sb.Execute("edit_file", map[string]any{"path": p, "old_str": "a", "new_str": "b"})
		if !strings.HasPrefix(out, "ERROR") || !strings.Contains(out, "protected") {
			t.Fatalf("%s must be edit-protected: %q", p, out)
		}
	}
	// Normal files unaffected.
	if out := sb.Execute("write_file", map[string]any{"path": "main.go", "content": "package main"}); !strings.HasPrefix(out, "OK") {
		t.Fatalf("normal write must pass: %q", out)
	}
}

func TestEvalPreflightDirtyTree(t *testing.T) {
	dir := initGitRepo(t)
	// Valid eval file so we reach the preflight.
	evalPath := filepath.Join(dir, "e.json")
	os.WriteFile(evalPath, []byte(`[{"name":"x","prompt":"p","check":"true"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "f.go"), []byte("dirty\n"), 0o644) // uncommitted change
	oldForce, oldVerify := forceEval, verifyCommand
	forceEval, verifyCommand = false, "true"
	defer func() { forceEval, verifyCommand = oldForce, oldVerify }()
	if code := runEval(dir, evalPath, 1); code != 1 {
		t.Fatalf("dirty tree must refuse, got exit %d", code)
	}
}

func TestEvalPreflightBrokenBaseline(t *testing.T) {
	dir := initGitRepo(t) // clean tree
	evalPath := filepath.Join(dir, "e.json")
	os.WriteFile(evalPath, []byte(`[{"name":"x","prompt":"p","check":"true"}]`), 0o644)
	gitRun(dir, "add", "-A")
	gitRun(dir, "commit", "-qm", "test: add eval file")
	oldForce, oldVerify := forceEval, verifyCommand
	forceEval, verifyCommand = false, "false" // baseline "build" always fails
	defer func() { forceEval, verifyCommand = oldForce, oldVerify }()
	if code := runEval(dir, evalPath, 1); code != 1 {
		t.Fatalf("broken baseline must refuse, got exit %d", code)
	}
}

func TestSessionFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := newSessionStore(t.TempDir())
	msgs := []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "original work"}}
	st.Append(msgs)
	origPath := st.Path()
	name, err := st.Fork(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, "-fork") || st.Path() == origPath {
		t.Fatalf("fork must switch to a new -fork file: %s vs %s", name, origPath)
	}
	orig, _ := os.ReadFile(origPath)
	forked, _ := os.ReadFile(st.Path())
	if !strings.Contains(string(forked), "original work") {
		t.Fatal("fork must carry the full history")
	}
	if len(orig) == 0 {
		t.Fatal("original session must remain intact")
	}
	// Writes after the fork go only to the fork.
	st.Append(append(msgs, Message{Role: "user", Content: "risky new direction"}))
	orig2, _ := os.ReadFile(origPath)
	if strings.Contains(string(orig2), "risky new direction") {
		t.Fatal("post-fork writes must not touch the original")
	}
}

func TestSelfKnowledgeInPrompt(t *testing.T) {
	p := buildSystemPrompt(t.TempDir())
	for _, want := range []string{".bak", "checkpoint", "USER commands", "diff"} {
		if !strings.Contains(p, want) {
			t.Fatalf("system prompt missing harness fact %q", want)
		}
	}
}
