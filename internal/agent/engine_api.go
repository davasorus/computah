package agent

// engine_api.go is the exported surface the presentation packages (tui, web)
// use. It deliberately exposes SNAPSHOTS and methods, never the engine's
// internal mutexes or mutable globals, so package boundaries stay clean.

// StatsSnapshot is an immutable view of the cumulative request stats, safe to
// read from another package without touching the recorder's lock.
type StatsSnapshot struct {
	Requests   int
	PromptTk   int
	GenTk      int
	ThinkTk    int
	LastTTFBms int64
}

// Stats returns a locked snapshot of the current stats recorder.
func Stats() StatsSnapshot {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	return StatsSnapshot{
		Requests:   stats.requests,
		PromptTk:   stats.promptTk,
		GenTk:      stats.genTk,
		ThinkTk:    stats.thinkTk,
		LastTTFBms: stats.lastTTFB.Milliseconds(),
	}
}

// TodoItem is one entry in the agent's working todo list.
type TodoItem = todoItem

// Todos returns a copy of the current todo list.
func Todos() []TodoItem {
	out := make([]TodoItem, len(todos))
	copy(out, todos)
	return out
}

// ApprovalDecision mirrors the internal approval outcome type for the web UI.
type ApprovalDecision = approvalDecision

// AnswerWebApproval resolves a pending browser-routed approval. Returns true if
// the id matched a pending request.
func AnswerWebApproval(id string, d ApprovalDecision) bool {
	return approvals.answerWeb(id, d)
}

// SetApprovalWeb routes tool approvals to the browser (headless mode).
func SetApprovalWeb() { setApprovalMode(approvalWeb) }

var UseTTY = useTTY

// Approval decision constants for the web UI.
const (
	ApproveOnce   = approveOnce
	ApproveAlways = approveAlways
	ApproveDeny   = approveDeny
)

// NewSessionStore creates a session store rooted at the given directory.
func NewSessionStore(root string) *SessionStore { return newSessionStore(root) }

// CompleteLine exposes the REPL tab-completer for the TUI input.
func CompleteLine(line string, pos int, key rune) (string, int, bool) {
	return completeLine(line, pos, key)
}

// SilenceStdout tells the stdout subscriber whether to suppress output (the
// TUI owns the screen and silences it).
func SilenceStdout(silent bool) { stdoutSub.silent = silent }

// VerifyCommand returns the configured post-turn verify command ("" if none).
func VerifyCommand() string { return verifyCommand }

// SetTodos replaces the current todo list. Primarily for tests and headless
// setup; normal updates flow through the update_todos tool.
func SetTodos(items []TodoItem) { todos = items }
