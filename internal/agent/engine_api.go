package agent

import "net/http"

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

// --- tool-plugin API: lets tool packages (decision, structured, embed) live
// outside the engine while still registering into its tool registry. ---

// ToolArgs is the argument map passed to a tool handler.
type ToolArgs = toolArgs

// Resolve resolves a possibly-relative path against the sandbox root, applying
// the sandbox's safety checks. Exported for out-of-package tool handlers.
func (s *Sandbox) Resolve(p string) (string, error) { return s.resolve(p) }

// Backup snapshots a file before modification (for undo). Exported for
// out-of-package tool handlers.
func (s *Sandbox) Backup(path string) (bool, error) { return s.backup(path) }

// RegisterTools adds tools to the engine's registry (idempotent by name).
func RegisterTools(ts ...Tool) { registerTools(ts...) }

// MarkReadOnly flags a tool name as read-only (safe under plan mode, eligible
// for parallel execution).
func MarkReadOnly(name string) { readOnlyTools[name] = true }

// ToolRegistrations are functions that register tools into the engine at
// startup. Tool packages append their registrar here (via cmd wiring) so the
// engine never imports them. Run invokes each after built-ins are registered.
var ToolRegistrations []func()

// runToolRegistrations invokes every registered tool-provider hook.
func runToolRegistrations() {
	for _, reg := range ToolRegistrations {
		if reg != nil {
			reg()
		}
	}
}

// RebuildToolSchemas regenerates the cached JSON schemas after tools are
// registered. Tool packages call this at the end of their registrar.
func RebuildToolSchemas() { buildToolSchemas() }

// Str returns the string value for key (exported accessor for out-of-package
// tool handlers).
func (a ToolArgs) Str(key string) string { return toolArgs(a).str(key) }

// Num returns the int value for key.
func (a ToolArgs) Num(key string) int { return toolArgs(a).num(key) }

// HTTPClient is the shared HTTP client (long timeout for model calls),
// exported for tool packages that make their own requests.
func HTTPClient() *http.Client { return httpClient }

// --- more engine internals exposed for out-of-package tool handlers ---

// CurBaseURL returns the current model server base URL.
func CurBaseURL() string { return curBaseURL }

// IsProtected reports whether a repo-relative path is protected from edits.
func IsProtected(rel string) bool { return isProtected(rel) }

// ClosestLines returns the lines in content most similar to needle (for edit
// rejection diagnostics).
func ClosestLines(content, needle string) string { return closestLines(content, needle) }

// WriteAtomic writes data to path atomically (temp + rename).
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }

// RunHook runs a configured lifecycle hook by name.
func RunHook(name string, repl map[string]string) (string, bool) { return runHook(name, repl) }

// --- registry test-support (used by tool-package tests) ---

// RegistrySnapshot captures the tool registry state for save/restore in tests.
type RegistrySnapshot struct {
	reg []Tool
	idx map[string]Tool
}

// SnapshotRegistry returns the current registry state.
func SnapshotRegistry() RegistrySnapshot {
	idx := make(map[string]Tool, len(toolByName))
	for k, v := range toolByName {
		idx[k] = v
	}
	return RegistrySnapshot{reg: append([]Tool(nil), registry...), idx: idx}
}

// RestoreRegistry restores a previously captured registry state.
func RestoreRegistry(s RegistrySnapshot) { registry, toolByName = s.reg, s.idx }

// ResetRegistry clears the registry (tests set up a known state after).
func ResetRegistry() { registry, toolByName = nil, map[string]Tool{} }

// LookupTool returns a registered tool by name.
func LookupTool(name string) (Tool, bool) { t, ok := toolByName[name]; return t, ok }

// DeleteTool removes a tool from the registry by name.
func DeleteTool(name string) { delete(toolByName, name) }

// UnregisterTools removes tools by name from the registry (used by tests and
// the vault-dedup path).
func UnregisterTools(names ...string) { unregisterTools(names...) }

// SetHooks replaces the lifecycle hooks map (used by tests).
func SetHooks(h map[string]string) { hooks = h }

// Hooks returns the current lifecycle hooks map (used by tests to save/restore).
func Hooks() map[string]string { return hooks }
