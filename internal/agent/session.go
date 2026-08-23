package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- Session persistence ----------
//
// Every turn is autosaved as JSONL (one Message per line) under
// ~/.agent/sessions/<hash-of-workdir>/<timestamp>.jsonl — appending is O(1)
// and a crash mid-write loses at most one line, not the session.
// -resume loads the tail of the latest session for this directory;
// /compact distills the session into a summary and starts a fresh file.

const resumeTail = 30 // messages restored by -resume (full transcript stays on disk)

// autoCompactTokens is the soft context budget. When the running estimate
// crosses it, the session auto-compacts before the next turn. Set well below
// the model's real window (262k for Gemma 4) to leave room for the reply and
// because the estimate is rough. Override with AGENT_COMPACT_TOKENS.
var autoCompactTokens = func() int {
	if v := os.Getenv("AGENT_COMPACT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 100_000
}()

// estimateTokens is a cheap heuristic: ~4 chars per token across all message
// content and tool-call arguments. Not exact, but the auto-compact threshold
// only needs an order-of-magnitude signal, and this avoids a tokenizer dep.
func estimateTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Content)
		for _, tc := range m.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return chars / 4
}

// pruneStaleReads elides read_file results for files that were modified
// LATER in the conversation. This is a correctness measure, not just a
// budget one: a stale read of a since-edited file is actively misleading
// context — the model can "remember" code that no longer exists and produce
// edit_file calls against it. The tool message keeps its id (transcript
// stays valid) with a marker telling the model to re-read.
func pruneStaleReads(messages []Message) int {
	// Map every tool_call_id to its call name + path argument.
	type callInfo struct {
		name string
		path string
	}
	calls := map[string]callInfo{}
	// Last index at which each path was modified (write_file/edit_file, by
	// the model or a subtask). Paths are compared as raw argument strings —
	// a read as "cmd/root.go" and a write as an absolute path won't match,
	// so this prunes conservatively; exact-match misses are just kept.
	lastMod := map[string]int{}
	for i, m := range messages {
		for _, tc := range m.ToolCalls {
			var a struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &a)
			calls[tc.ID] = callInfo{name: tc.Function.Name, path: a.Path}
			if (tc.Function.Name == "write_file" || tc.Function.Name == "edit_file") && a.Path != "" {
				lastMod[a.Path] = i
			}
		}
	}
	const marker = "[stale: this file was modified later in the session — re-read it if you need its current content]"
	pruned := 0
	for i := range messages {
		m := &messages[i]
		if m.Role != "tool" || m.Content == marker {
			continue
		}
		ci, ok := calls[m.ToolCallID]
		if !ok || ci.name != "read_file" || ci.path == "" {
			continue
		}
		if modIdx, modified := lastMod[ci.path]; modified && modIdx > i && len(m.Content) > len(marker) {
			m.Content = marker
			pruned++
		}
	}
	return pruned
}

// manageContext is the mid-turn pressure valve, and it runs as ONE batch for
// a reason: LM Studio (llama.cpp) reuses the KV cache only for the longest
// UNCHANGED PREFIX of the request. Mutating message 5 invalidates the cache
// for everything after message 5, so the next request reprocesses ~all of
// the context from scratch — minutes at local prompt-processing speeds.
// Editing history is therefore expensive and should happen rarely and all at
// once (prune + shrink together, one prefix invalidation), never a little
// each turn. Below the budget, this function must not touch anything.
func manageContext(messages []Message) []Message {
	before := estimateTokens(messages)
	if before <= autoCompactTokens {
		return messages
	}
	pruned := pruneStaleReads(messages)
	messages = shrinkOldToolResults(messages)
	after := estimateTokens(messages)
	if after < before {
		fmt.Println(tint(cDim, fmt.Sprintf(
			"  (context ~%dk tokens — pruned %d stale read(s), elided older tool outputs → ~%dk; note: the next request reprocesses the prompt)",
			before/1000, pruned, after/1000)))
	}
	if after > autoCompactTokens {
		fmt.Println(tint(cYellow, "  (context still over budget after eliding — /compact when this turn finishes, or raise compact_tokens)"))
	}
	return messages
}

// shrinkOldToolResults elides the bodies of all but the most recent tool
// results, keeping a head and tail of each so the model still sees what a
// call was and roughly what it returned. This is the mid-turn pressure valve:
// full /compact restarts the session and can't safely run while a tool loop
// is in flight, but eliding old outputs keeps the transcript structurally
// valid (tool_call_ids untouched). Messages already persisted to the session
// file keep their full content on disk.
func shrinkOldToolResults(messages []Message) []Message {
	const (
		keepRecent = 4    // most recent tool results stay intact
		threshold  = 2048 // only elide results longer than this
		headKeep   = 1024
		tailKeep   = 256
	)
	// Find the cutoff: everything before the keepRecent-th most recent tool
	// message is fair game.
	cut, seen := 0, 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" {
			seen++
			if seen == keepRecent {
				cut = i
				break
			}
		}
	}
	for i := 0; i < cut; i++ {
		m := &messages[i]
		if m.Role != "tool" || len(m.Content) <= threshold {
			continue
		}
		elided := len(m.Content) - headKeep - tailKeep
		m.Content = m.Content[:headKeep] +
			fmt.Sprintf("\n...[%d chars elided to save context — re-run the tool if needed]...\n", elided) +
			m.Content[len(m.Content)-tailKeep:]
	}
	return messages
}

type SessionStore struct {
	dir       string
	name      string // optional fixed filename (evals); default = timestamp
	file      *os.File
	persisted int // messages already written to the current file
}

// newSessionStore opens a per-directory session store under
// ~/.agent/sessions/<workdir-hash>/. Any failure returns a disabled store —
// persistence is a convenience and must never block actual work.
func newSessionStore(workdir string) *SessionStore {
	home, err := os.UserHomeDir()
	if err != nil {
		return &SessionStore{} // persistence disabled, agent still works
	}
	h := sha256.Sum256([]byte(workdir))
	dir := filepath.Join(home, ".agent", "sessions", hex.EncodeToString(h[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &SessionStore{}
	}
	return &SessionStore{dir: dir}
}

// Append persists any messages beyond what's already on disk.
func (st *SessionStore) Append(messages []Message) {
	if st.dir == "" {
		return
	}
	if st.file == nil {
		base := st.name
		if base == "" {
			base = time.Now().Format("20060102-150405")
		}
		name := filepath.Join(st.dir, base+".jsonl")
		f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			st.dir = "" // disable quietly rather than nag every turn
			return
		}
		st.file = f
	}
	enc := json.NewEncoder(st.file)
	for ; st.persisted < len(messages); st.persisted++ {
		if err := enc.Encode(messages[st.persisted]); err != nil {
			return
		}
	}
}

// Rotate closes the current file so the next Append starts a fresh session
// (used by /compact so the compacted context begins a new transcript).
func (st *SessionStore) Rotate() {
	if st.file != nil {
		_ = st.file.Close()
		st.file = nil
	}
	st.persisted = 0
}

func (st *SessionStore) Path() string {
	if st.file != nil {
		return st.file.Name()
	}
	return st.dir
}

// latestSession returns the newest .jsonl in the store's directory.
func (st *SessionStore) latestSession() (string, error) {
	if st.dir == "" {
		return "", fmt.Errorf("session persistence unavailable")
	}
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") && !strings.HasPrefix(e.Name(), "eval-") {
			names = append(names, e.Name()) // eval-* transcripts are diagnostics, not conversations
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no previous sessions for this directory")
	}
	sort.Strings(names) // timestamp-named, so lexical order = chronological
	return filepath.Join(st.dir, names[len(names)-1]), nil
}

// loadSession reads a JSONL transcript and returns its last `tail` messages,
// excluding system messages (a fresh one is always built at startup) and
// trimming leading orphaned tool results (a tool message without its
// preceding assistant tool_calls message is rejected by the API).
func loadSession(path string, tail int) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var msgs []Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			break // truncated final line from a crash — keep what parsed
		}
		if m.Role == "system" {
			continue
		}
		msgs = append(msgs, m)
	}
	if len(msgs) > tail {
		msgs = msgs[len(msgs)-tail:]
	}
	for len(msgs) > 0 && msgs[0].Role == "tool" {
		msgs = msgs[1:]
	}
	// Trim an incomplete trailing tool-call group: a crash mid-turn can leave
	// an assistant tool_calls message with some or all of its tool results
	// missing, which servers reject as a malformed transcript. Drop the whole
	// group unless every call has its result.
	for len(msgs) > 0 {
		i := len(msgs)
		nResults := 0
		for i > 0 && msgs[i-1].Role == "tool" {
			i--
			nResults++
		}
		if i > 0 && len(msgs[i-1].ToolCalls) > 0 {
			if len(msgs[i-1].ToolCalls) == nResults {
				break // complete group — transcript is valid
			}
			msgs = msgs[:i-1] // partial results: drop the assistant call + strays
			continue
		}
		if nResults > 0 {
			msgs = msgs[:i] // tool results with no assistant carrier
			continue
		}
		break
	}
	return msgs, nil
}

// listSessions prints the sessions available for this working directory,
// newest first, with a preview of each session's first user message.
// ---------- Session titles ----------
//
// Timestamp filenames tell you nothing at /resume pick time. A one-line
// title is generated by the model when a session with real content ends,
// stored as a sidecar <session>.title file (the .jsonl transcript format
// stays untouched and forward-compatible).

// titlePath returns the sidecar title file for a session jsonl path.
func titlePath(sessionPath string) string {
	return strings.TrimSuffix(sessionPath, ".jsonl") + ".title"
}

func sessionTitle(sessionPath string) string {
	data, err := os.ReadFile(titlePath(sessionPath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// generateTitle asks the model for a one-line session title and writes the
// sidecar. Best-effort and quiet: run at exit, never worth an error.
func (st *SessionStore) generateTitle(messages []Message) {
	if st.file == nil || sessionTitle(st.Path()) != "" {
		return
	}
	// Only sessions with real content deserve a title (and a model call).
	var userMsgs []string
	for _, m := range messages {
		if m.Role == "user" && !strings.HasPrefix(m.Content, "[") {
			c := m.Content
			if len(c) > 200 {
				c = c[:200]
			}
			userMsgs = append(userMsgs, c)
		}
	}
	if len(userMsgs) == 0 {
		return
	}
	if len(userMsgs) > 8 {
		userMsgs = userMsgs[:8]
	}
	req := []Message{
		{Role: "system", Content: "Summarize what this coding session was about in ONE line, at most 8 words, no punctuation at the end, no quotes. Respond with the title only."},
		{Role: "user", Content: strings.Join(userMsgs, "\n---\n")},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reply, err := chat(ctx, curBaseURL, auxModelFor(), req, func(string) {})
	if err != nil {
		return
	}
	title := strings.TrimSpace(strings.Trim(strings.TrimSpace(reply.Content), "\"'`"))
	if title == "" || strings.Contains(title, "\n") {
		return
	}
	if len(title) > 80 {
		title = title[:80]
	}
	_ = os.WriteFile(titlePath(st.Path()), []byte(title+"\n"), 0o644)
}

// Fork starts a NEW session file seeded with the current conversation and
// switches persistence to it — the original file stays frozen where it is,
// so a risky direction can be explored and abandoned by /resume-ing the
// original. Returns the new session name.
func (st *SessionStore) Fork(messages []Message) (string, error) {
	if st.dir == "" {
		return "", fmt.Errorf("session persistence unavailable")
	}
	if st.file != nil {
		_ = st.file.Close()
	}
	parent := filepath.Base(st.Path())
	st.name = time.Now().Format("20060102-150405") + "-fork"
	st.file = nil
	st.persisted = 0
	st.Append(messages) // opens the new file and writes the full history
	if st.file == nil {
		return "", fmt.Errorf("could not create fork session file")
	}
	// Record parentage so /tree can render the branch structure.
	_ = os.WriteFile(strings.TrimSuffix(st.Path(), ".jsonl")+".parent", []byte(parent+"\n"), 0o644)
	return filepath.Base(st.Path()), nil
}

// sessionParent returns the parent session filename for a fork, or "".
func (st *SessionStore) sessionParent(name string) string {
	data, err := os.ReadFile(filepath.Join(st.dir, strings.TrimSuffix(name, ".jsonl")+".parent"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// printSessionTree renders sessions as a parent/child forest (for /tree).
func (st *SessionStore) printSessionTree() {
	names := st.sessionNames()
	if len(names) == 0 {
		fmt.Println("no sessions for this directory")
		return
	}
	parent := map[string]string{}
	children := map[string][]string{}
	isChild := map[string]bool{}
	for _, n := range names {
		if p := st.sessionParent(n); p != "" {
			parent[n] = p
			children[p] = append(children[p], n)
			isChild[n] = true
		}
	}
	var roots []string
	for _, n := range names {
		if !isChild[n] {
			roots = append(roots, n)
		}
	}
	var walk func(name, indent string)
	walk = func(name, indent string) {
		label := strings.TrimSuffix(name, ".jsonl")
		if t := sessionTitle(filepath.Join(st.dir, name)); t != "" {
			label += "  " + tint(cDim, "“"+t+"”")
		}
		fmt.Println(indent + label)
		kids := children[name]
		sort.Strings(kids)
		for _, k := range kids {
			walk(k, indent+"  └─ ")
		}
	}
	sort.Strings(roots)
	for _, r := range roots {
		walk(r, "  ")
	}
}

// sessionNames returns this directory's session files, newest first.
func (st *SessionStore) sessionNames() []string {
	if st.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") && !strings.HasPrefix(e.Name(), "eval-") {
			names = append(names, e.Name()) // eval-* transcripts are diagnostics, not conversations
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // newest first
	return names
}

func (st *SessionStore) listSessions() {
	if st.dir == "" {
		fmt.Println("session persistence unavailable")
		return
	}
	names := st.sessionNames()
	if len(names) == 0 {
		fmt.Println("no sessions saved for this directory yet")
		return
	}
	fmt.Printf("sessions for this directory (%s):\n", st.dir)
	for i, name := range names {
		msgs, _ := loadSession(filepath.Join(st.dir, name), 1<<30)
		preview := "(empty)"
		for _, m := range msgs {
			if m.Role == "user" && !strings.HasPrefix(m.Content, "[") {
				preview = m.Content
				if len(preview) > 60 {
					preview = preview[:60] + "..."
				}
				break
			}
		}
		marker := ""
		if i == 0 {
			marker = "  <- latest"
		}
		label := preview
		if t := sessionTitle(filepath.Join(st.dir, name)); t != "" {
			label = t // model-written title beats a raw prompt excerpt
		}
		fmt.Printf("  %2d) %-24s %3d msgs  %q%s\n", i+1, strings.TrimSuffix(name, ".jsonl"), len(msgs), label, marker)
	}
	fmt.Println("resume with: -resume latest, -resume <name>, -resume pick, or /resume in-session")
}

// pickSession lists sessions numbered and asks which to load. Returns the
// chosen file path, or an error (including "skipped" on empty input).
func (st *SessionStore) pickSession() (string, error) {
	names := st.sessionNames()
	if len(names) == 0 {
		return "", fmt.Errorf("no previous sessions for this directory")
	}
	st.listSessions()
	ans, ok := askLine("resume which? (number, Enter to skip) ")
	if !ok || ans == "" {
		return "", fmt.Errorf("skipped")
	}
	n, err := strconv.Atoi(ans)
	if err != nil || n < 1 || n > len(names) {
		return "", fmt.Errorf("no session %q — expected 1-%d", ans, len(names))
	}
	return filepath.Join(st.dir, names[n-1]), nil
}

// resolveSession maps a -resume value to a session file path:
// "latest" -> newest session; anything else -> a session name (with or
// without the .jsonl suffix).
func (st *SessionStore) resolveSession(arg string) (string, error) {
	if arg == "latest" {
		return st.latestSession()
	}
	if st.dir == "" {
		return "", fmt.Errorf("session persistence unavailable")
	}
	name := arg
	if !strings.HasSuffix(name, ".jsonl") {
		name += ".jsonl"
	}
	path := filepath.Join(st.dir, name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no session %q for this directory (see /sessions)", arg)
	}
	return path, nil
}

// compact distills the conversation into a summary written by the model
// itself, then restarts the context (and the session file) from that
// summary. The full transcript remains on disk in the rotated-out file.
// This resets the KV-cache prefix — the first request after a compact
// reprocesses from scratch, which is the one-time price of a small context.
func compact(baseURL, model string, messages []Message, st *SessionStore) []Message {
	if len(messages) <= 1 {
		fmt.Println("(nothing to compact yet)")
		return messages
	}
	messages = append(messages, Message{
		Role: "user",
		Content: "Summarize this session for future context: key decisions, files changed and why, " +
			"and any unresolved threads. Be concise (under 300 words). Output only the summary.",
	})
	reply, intr, err := streamChat(baseURL, model, messages)
	if intr {
		fmt.Println("\n(compact interrupted — session unchanged)")
		return messages[:len(messages)-1] // drop the summary request
	}
	if err != nil {
		fmt.Println("compact failed:", err)
		return messages[:len(messages)-1] // drop the summary request, keep the session
	}
	fmt.Println()
	fresh := []Message{
		messages[0], // the system prompt
		{Role: "user", Content: "Context carried over from a previous session (compacted summary):\n" + reply.Content},
		{Role: "assistant", Content: "Understood — continuing from that context."},
	}
	st.Rotate()
	st.Append(fresh)
	fmt.Println("(session compacted — context reset to the summary; full transcript kept on disk)")
	return fresh
}
