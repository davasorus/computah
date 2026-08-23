package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------- Tool registry ----------
//
// One source of truth per tool: its schema AND its handler live together in
// a Tool. registry() builds the list; toolSchemas() derives the API payload
// and Sandbox.Execute() dispatches by name — so the schema the model sees
// and the code that runs can never drift apart.

type Tool struct {
	Name     string
	Desc     string
	Props    map[string]any
	Required []string
	// RawSchema, when set, is used verbatim as the tool's JSON-schema
	// parameters (MCP servers ship their own); Props/Required are ignored.
	RawSchema map[string]any
	Handler   func(s *Sandbox, args toolArgs) string
}

// toolArgs wraps the decoded argument map with typed accessors.
type toolArgs map[string]any

func (a toolArgs) str(key string) string {
	v, _ := a[key].(string)
	return v
}
func (a toolArgs) num(key string) int {
	if v, ok := a[key].(float64); ok {
		return int(v)
	}
	return 0
}

// registry is populated in tools.go-style init below; toolByName indexes it.
var registry []Tool
var toolByName = map[string]Tool{}

func registerTools(ts ...Tool) {
	for _, t := range ts {
		if _, exists := toolByName[t.Name]; !exists {
			registry = append(registry, t)
		} else {
			// Replace in place so re-registration (tests, re-init) updates
			// rather than duplicating — a duplicate would list twice in
			// /tools and bloat the schema payload.
			for i := range registry {
				if registry[i].Name == t.Name {
					registry[i] = t
					break
				}
			}
		}
		toolByName[t.Name] = t
	}
}

// unregisterTools removes tools by name from the registry and index — used
// to suppress redundant tools (e.g. the file-layer vault tools when the MCP
// vault server supersedes them). buildToolSchemas must be called after.
func unregisterTools(names ...string) {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
		delete(toolByName, n)
	}
	kept := registry[:0]
	for _, t := range registry {
		if !drop[t.Name] {
			kept = append(kept, t)
		}
	}
	registry = kept
}

// tools is the JSON-schema payload sent to the model, derived from registry.
var tools []map[string]any

func buildToolSchemas() {
	tools = tools[:0]
	for _, t := range registry {
		if t.RawSchema != nil {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Desc,
					"parameters":  sanitizeSchema(t.RawSchema),
				},
			})
			continue
		}
		tools = append(tools, toolDef(t.Name, t.Desc, t.Props, t.Required))
	}
}

// sanitizeSchema normalizes a JSON Schema object for strict server-side
// validators (LM Studio rejects tools/list payloads where required is null
// rather than an array, or properties is absent). MCP servers and the SDK
// legitimately omit these, so we fill them in: an object schema always gets
// a properties object and a required array. Returns a shallow copy so the
// source schema is untouched.
func sanitizeSchema(s map[string]any) map[string]any {
	out := make(map[string]any, len(s)+2)
	for k, v := range s {
		out[k] = v
	}
	if out["type"] == nil {
		out["type"] = "object"
	}
	if out["type"] == "object" {
		if _, ok := out["properties"].(map[string]any); !ok {
			out["properties"] = map[string]any{}
		}
		// required must be an array — never null/absent for strict validators.
		switch out["required"].(type) {
		case []any, []string:
			// already an array; leave it
		default:
			out["required"] = []string{}
		}
	}
	return out
}

func init() {
	registerTools(
		Tool{
			Name: "read_file",
			Desc: "Read a text file. Relative paths resolve against the working directory; absolute paths are allowed. For very large files, use offset/limit to read a window.",
			Props: map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path to the file"},
				"offset": map[string]any{"type": "integer", "description": "1-based line to start from (optional)"},
				"limit":  map[string]any{"type": "integer", "description": "Max lines to return (optional)"},
			},
			Required: []string{"path"},
			Handler:  (*Sandbox).toolReadFile,
		},
		Tool{
			Name: "write_file",
			Desc: "Create a new file, or fully overwrite an existing one. Parent directories are created automatically. Content must be the complete, final file. For changing part of an existing file, prefer edit_file. When overwriting, the result includes a diff against the previous content — read it and confirm the change matches your intent.",
			Props: map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file (not a directory)"},
				"content": map[string]any{"type": "string", "description": "Full, real file content to write"},
			},
			Required: []string{"path", "content"},
			Handler:  (*Sandbox).toolWriteFile,
		},
		Tool{
			Name: "edit_file",
			Desc: "Replace old_str with new_str in a file. By default old_str must appear exactly once (matching whitespace exactly). Set replace_all=true to replace every occurrence. Preferred over write_file for modifying existing files. The result includes a diff of the change — read it and confirm it matches your intent before moving on; if it does not, correct it immediately.",
			Props: map[string]any{
				"path":        map[string]any{"type": "string", "description": "Path to the file to edit"},
				"old_str":     map[string]any{"type": "string", "description": "Exact existing text to replace"},
				"new_str":     map[string]any{"type": "string", "description": "Replacement text (empty string deletes old_str)"},
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring uniqueness (optional, default false)"},
			},
			Required: []string{"path", "old_str", "new_str"},
			Handler:  (*Sandbox).toolEditFile,
		},
		Tool{
			Name: "list_dir",
			Desc: "List files and subdirectories at a path. Use \".\" for the working directory.",
			Props: map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path, \".\" for the working directory"},
			},
			Required: []string{"path"},
			Handler:  (*Sandbox).toolListDir,
		},
		Tool{
			Name: "search_files",
			Desc: "Search file contents with a regular expression, like grep -rn. Returns matching lines as path:line: text. Skips .git, node_modules, and binary files.",
			Props: map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Go/RE2 regular expression to search for"},
				"path":    map[string]any{"type": "string", "description": "Directory to search (default: the working directory)"},
			},
			Required: []string{"pattern"},
			Handler:  (*Sandbox).toolSearchFiles,
		},
		Tool{
			Name: "tree",
			Desc: "Show a recursive directory tree from a path, depth-limited. Use this once to orient in a project instead of many list_dir calls. Skips .git, node_modules, and other noise.",
			Props: map[string]any{
				"path":  map[string]any{"type": "string", "description": "Root directory (default: the working directory)"},
				"depth": map[string]any{"type": "integer", "description": "Max depth to descend (default 3)"},
			},
			Required: []string{},
			Handler:  (*Sandbox).toolTree,
		},
		Tool{
			Name: "fetch_url",
			Desc: "Fetch the text content of an http(s) URL (documentation, API responses, raw source). HTML is returned as-is. The user approves each fetch. Use for looking up references the user points you to or that a task requires.",
			Props: map[string]any{
				"url": map[string]any{"type": "string", "description": "The http(s) URL to fetch"},
			},
			Required: []string{"url"},
			Handler:  (*Sandbox).toolFetchURL,
		},
		Tool{
			Name: "glob",
			Desc: "Find files by name pattern (like 'find -name'), recursively from a path. Pattern matches base names: '*.sql', 'main.*', 'Dockerfile'. Use this to locate files by name; use search_files to search file CONTENTS.",
			Props: map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Filename glob, e.g. '*.sql'"},
				"path":    map[string]any{"type": "string", "description": "Directory to search from (default: the working directory)"},
			},
			Required: []string{"pattern"},
			Handler:  (*Sandbox).toolGlob,
		},
		Tool{
			Name: "spawn_task",
			Desc: "Delegate a self-contained subtask to a fresh agent context with the same tools. Use for big multi-part work: each part runs without the parent's accumulated context, and only the final summary returns. Give a complete, standalone task description — the subtask cannot see this conversation. Optionally name a role (a defined specialist with its own instructions, possibly its own model).",
			Props: map[string]any{
				"task": map[string]any{"type": "string", "description": "Complete standalone description of the subtask, including any paths or requirements it needs"},
				"role": map[string]any{"type": "string", "description": "Optional named agent role from .agent/agents/ (ask the user, or omit for a plain subtask)"},
			},
			Required: []string{"task"},
			Handler:  toolSpawnTask,
		},
		Tool{
			Name: "update_todos",
			Desc: "Maintain a visible task checklist for multi-step work. Call it when starting a task with 3+ steps (all items pending), and again as each step completes (same list, done flags updated). The user sees the checklist in their terminal — keep item text short. Replaces the whole list each call.",
			Props: map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text": map[string]any{"type": "string", "description": "Short imperative step description"},
							"done": map[string]any{"type": "boolean", "description": "true once the step is complete"},
						},
						"required": []string{"text"},
					},
					"description": "The complete current checklist, in order",
				},
			},
			Required: []string{"todos"},
			Handler:  (*Sandbox).toolUpdateTodos,
		},
		Tool{
			Name: "run_command",
			Desc: "Propose a shell command to run in the working directory. Available: build/test tools, git, gh (GitHub CLI), and code (opens files/folders in VS Code). Read-only commands (git status/diff/log/show, gh pr list/view, code) run immediately; anything mutating (git add/commit/push, gh pr create, builds, deletions) asks the user to approve first. Output (stdout+stderr) and the exit code are returned.",
			Props: map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command to run"},
			},
			Required: []string{"command"},
			Handler:  (*Sandbox).toolRunCommand,
		},
	)
	buildToolSchemas()
}

// ---------- Command auto-approval ----------

// Read-only commands that run without the y/N prompt. Everything that
// mutates state — git add/commit/push, gh pr create, builds, deletions —
// still requires explicit user approval.
var autoApprovedPrefixes = []string{
	"git status", "git diff", "git log", "git show", "git ls-files",
	"git blame", "git stash list",
	"gh pr list", "gh pr view", "gh pr status", "gh issue list",
	"gh issue view", "gh repo view", "gh run list", "gh run view",
	// "code" is handled specially in autoApproved: allowed only without flags,
	// so "code ." opens silently but "code --install-extension x" prompts.
	// plain read-only shell commands (space-suffixed so "ls" can't match "lsblk")
	"ls ", "cat ", "head ", "tail ", "wc ", "stat ", "file ", "tree ",
	"which ", "grep ", "du ", "df ",
}

var autoApprovedExact = []string{
	"git branch", "git remote -v", "code",
	"ls", "pwd", "tree", "df", "du",
}

// allowFile is the user-extensible allowlist: one command prefix per line,
// '#' for comments. It is re-read on EVERY approval check, so edits from
// another terminal (or via the /allow command) apply immediately, mid-session.
func allowFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agent", "allow.txt")
}

func userAllowPrefixes() []string {
	path := allowFile()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// appendAllow adds a prefix to the user allowlist file; effective on the
// very next approval check. Returns an error message or "".
func appendAllow(prefix string) string {
	path := allowFile()
	if path == "" {
		return "cannot locate home directory; allowlist file unavailable"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err.Error()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(f, prefix)
	return ""
}

// autoApproved reports whether cmd may run without confirmation. Any shell
// chaining or substitution disqualifies it — "git status && rm -rf /" must
// not ride the allowlist.
// autoApproved reports whether a command runs without the y/N prompt: the
// BUILT-IN read-only rules, or the user's personal allowlist. The two are
// deliberately separate functions because they mean different things —
// built-in approval means "side-effect-free"; user approval means only "the
// user is tired of confirming this". The repeat-breaker's idempotence check
// must use builtinAutoApproved: a user who allowlisted "go " has approved
// "go run . migrate" for CONVENIENCE, not declared it free of side effects.
func autoApproved(cmd string) bool {
	if builtinAutoApproved(cmd) {
		return true
	}
	// Structural disqualifiers apply to user prefixes too.
	if strings.ContainsAny(cmd, ";|`<>\n") || strings.Contains(cmd, "$(") || strings.Contains(cmd, "&") {
		return false
	}
	for _, p := range userAllowPrefixes() {
		if strings.HasPrefix(cmd, p) {
			return true
		}
	}
	return false
}

// builtinAutoApproved is the read-only ruleset only — no user allowlist.
func builtinAutoApproved(cmd string) bool {
	// Absolute disqualifiers: substitution and redirection are never safe.
	if strings.ContainsAny(cmd, ";|`<>\n") || strings.Contains(cmd, "$(") {
		return false
	}
	// A && chain is approved only if EVERY segment is approved on its own —
	// "git status && rm -rf /" fails on the second segment. Chain segments
	// go through autoApproved (not builtin) so user prefixes still count for
	// PROMPTING purposes; idempotence callers never see chains approved this
	// way because the top-level builtin check re-evaluates the whole string.
	if strings.Contains(cmd, "&&") {
		for _, part := range strings.Split(cmd, "&&") {
			part = strings.TrimSpace(part)
			if part == "" || !builtinAutoApproved(part) {
				return false
			}
		}
		return true
	}
	if strings.Contains(cmd, "&") {
		return false // bare backgrounding is not auto-approvable
	}
	for _, e := range autoApprovedExact {
		if cmd == e {
			return true
		}
	}
	// "code" opens files/folders in VS Code — auto-approve only when no
	// argument is a flag, so "code ." runs silently but --install-extension,
	// --command, etc. still prompt. A user allowlist entry can override.
	if strings.HasPrefix(cmd, "code ") {
		flagged := false
		for _, f := range strings.Fields(cmd)[1:] {
			if strings.HasPrefix(f, "-") {
				flagged = true
				break
			}
		}
		if !flagged {
			return true
		}
	}
	for _, p := range autoApprovedPrefixes {
		if strings.HasPrefix(cmd, p) {
			return true
		}
	}
	return false
}

// ---------- Hooks ----------
//
// User-defined shell commands at lifecycle points — how invariants get
// enforced WITHOUT spending model turns on them: gofmt after every edit,
// a lint gate before every command, a status refresh after every turn.
//
//	"hooks": {
//	  "post_edit":   "gofmt -w {file}",   // after write_file/edit_file; {file} = absolute path
//	  "pre_command": "true",              // before run_command; nonzero exit BLOCKS the command ({cmd} available)
//	  "post_turn":   "git status -sb"     // after every completed turn
//	}
var hooks map[string]string

// curRoot is the working directory (set in main); hooks resolve against it.
var curRoot = "."

// ---------- Protected paths ----------
//
// Some files should be un-writable by the agent no matter what got
// approved: key material above all. Defaults cover cryptographic secrets;
// config "protected" adds patterns (matched with filepath.Match against
// both the relative path and the bare filename). This guards write_file,
// edit_file, and /undo restores. HONEST LIMIT: run_command is not filtered
// — a shell command can touch anything the user approves; the pre_command
// hook is the lever for policing commands.
var protectedPatterns = []string{"*.pem", "*.key", "id_rsa*", "id_ed25519*", "*.pfx", "*.p12", "*.keystore"}

func isProtected(rel string) bool {
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	for _, pat := range protectedPatterns {
		if ok, _ := filepath.Match(pat, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
	}
	return false
}

// runHook executes a configured hook. Returns the output and whether it
// succeeded; (_, true) when no such hook is configured.
func runHook(name string, repl map[string]string) (string, bool) {
	cmdStr, ok := hooks[name]
	if !ok || strings.TrimSpace(cmdStr) == "" {
		return "", true
	}
	for k, v := range repl {
		cmdStr = strings.ReplaceAll(cmdStr, "{"+k+"}", v)
	}
	out, code, err := execShell(cmdStr, curRoot, false)
	if err != nil || code != 0 {
		return out, false
	}
	return out, true
}

// ---------- Visible todo tracking ----------
//
// The model maintains a checklist via update_todos; the harness renders it
// as ☐/☑ in the trace so a long multi-step turn shows WHERE it is, and the
// model keeps itself on rails. Session-scoped; /todos reprints it.

type todoItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

var todos []todoItem

func (s *Sandbox) toolUpdateTodos(a toolArgs) string {
	raw, err := json.Marshal(a["todos"])
	if err != nil {
		return "ERROR: bad todos payload"
	}
	var items []todoItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return "ERROR: todos must be an array of {text, done}"
	}
	if len(items) == 0 {
		todos = nil
		return "OK: todo list cleared"
	}
	if len(items) > 20 {
		return "ERROR: keep the checklist to 20 items or fewer — group steps"
	}
	todos = items
	renderTodos()
	done := 0
	for _, t := range items {
		if t.Done {
			done++
		}
	}
	return fmt.Sprintf("OK: checklist updated — %d/%d done", done, len(items))
}

func renderTodos() {
	for _, t := range todos {
		if t.Done {
			fmt.Println(tint(cDim, "    ☑ "+t.Text))
		} else {
			fmt.Println("    ☐ " + t.Text)
		}
	}
}

// ---------- Tool execution ----------

// commandTimeout caps a single run_command execution. Overridable via
// command_timeout_sec in ~/.agent/config.json (applied in main) — bump it
// for projects whose full test suite or build legitimately runs long.
var commandTimeout = 5 * time.Minute

type Sandbox struct {
	Root     string
	Modified []string // absolute paths of every file written this session
}

func (s *Sandbox) resolve(p string) (string, error) {
	if p == "" {
		p = "."
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	return filepath.Abs(filepath.Join(s.Root, p))
}

// backup copies an existing file to <path>.bak; returns whether it existed.
// The backup is written only on the FIRST touch of a path each session, so
// <path>.bak always holds the pre-session original — a second bad write can
// never clobber the only good copy with the first bad write.
func (s *Sandbox) backup(path string) (bool, error) {
	old, err := os.ReadFile(path)
	if err != nil {
		return false, nil // no existing file, nothing to back up
	}
	for _, p := range s.Modified {
		if p == path {
			return true, nil // .bak already holds the pre-session content
		}
	}
	if err := os.WriteFile(path+".bak", old, 0o644); err != nil {
		return true, fmt.Errorf("could not create backup: %w", err)
	}
	return true, nil
}

// writeAtomic writes to a temp file in the same directory, then renames over
// the target. A crash mid-write leaves the original intact rather than a
// half-written file. Same-dir temp guarantees the rename is atomic (not a
// cross-device copy).
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".agent-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op if the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Execute dispatches a tool call by name through the registry. A panic in a
// handler is converted to an ERROR result instead of killing the process —
// the model sees the failure and the session (and its context) survives.
func (s *Sandbox) Execute(name string, args map[string]any) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf("ERROR: tool %s panicked: %v", name, r)
			fmt.Println(tint(cRed, "  ✗ "+result))
		}
	}()
	t, ok := toolByName[name]
	if !ok {
		return "ERROR: unknown tool " + name
	}
	if planMode && !readOnlyTools[name] && name != "update_todos" {
		fmt.Println(tint(cYellow, "  ✗ blocked (plan mode): "+name))
		return "ERROR: plan mode is read-only — no modifications until the user approves the plan. Include this step in the plan instead."
	}
	result = t.Handler(s, toolArgs(args))
	recordActivity(name, args, result) // session audit tally (human monitoring; not sent to the model)
	return result
}

func (s *Sandbox) toolReadFile(a toolArgs) string {
	path, err := s.resolve(a.str("path"))
	if err != nil {
		return "ERROR: " + err.Error()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	content := string(data)
	// Optional line window for huge files.
	if offset, limit := a.num("offset"), a.num("limit"); offset > 0 || limit > 0 {
		lines := strings.Split(content, "\n")
		start := 0
		if offset > 0 {
			start = offset - 1
		}
		if start >= len(lines) {
			return fmt.Sprintf("ERROR: offset %d beyond end of file (%d lines)", offset, len(lines))
		}
		end := len(lines)
		if limit > 0 && start+limit < end {
			end = start + limit
		}
		return fmt.Sprintf("(lines %d-%d of %d)\n%s", start+1, end, len(lines),
			strings.Join(lines[start:end], "\n"))
	}
	// Generous limit — with a 262k-context model, big files are fine;
	// this only guards against accidentally reading something enormous.
	const limit = 256 * 1024
	if len(content) > limit {
		return content[:limit] + "\n...[truncated at 256KB — use offset/limit to read further]"
	}
	return content
}

func (s *Sandbox) toolWriteFile(a toolArgs) string {
	rel := a.str("path")
	if isProtected(rel) {
		fmt.Println(tint(cRed, "  ✗ REJECTED write to "+rel+" (protected path)"))
		return "ERROR: " + rel + " matches a protected pattern (key material / user-configured). The agent may not write it; ask the user to change it themselves if needed."
	}
	// Models sometimes try to "create a directory" by writing a bare
	// directory path; that creates a file that then blocks the real
	// writes, so reject it with a corrective message.
	if strings.HasSuffix(rel, "/") || strings.HasSuffix(rel, "\\") {
		fmt.Println(tint(cRed, fmt.Sprintf("  ✗ REJECTED write to %s (directory path)", rel)))
		return "ERROR: path is a directory. Directories are created automatically when you write a file inside them; write a file instead."
	}
	// Empty or placeholder content is a hallucination tripwire.
	content := a.str("content")
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || (strings.HasPrefix(trimmed, "<") && strings.HasSuffix(trimmed, ">") && !strings.Contains(trimmed, "\n")) {
		fmt.Println(tint(cRed, fmt.Sprintf("  ✗ REJECTED write to %s (empty/placeholder content)", rel)))
		return "ERROR: empty or placeholder content rejected. Write the file's FULL, final content."
	}
	path, err := s.resolve(rel)
	if err != nil {
		fmt.Println(tint(cRed, fmt.Sprintf("  ✗ REJECTED write to %s (%v)", rel, err)))
		return "ERROR: " + err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "ERROR: " + err.Error()
	}
	prevData, _ := os.ReadFile(path) // pre-WRITE content for the diff (.bak holds pre-SESSION, which may be older)
	overwrote, err := s.backup(path)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	overwriteDiff := ""
	if err := writeAtomic(path, []byte(content)); err != nil {
		return "ERROR: " + err.Error()
	}
	s.Modified = append(s.Modified, path)
	if overwrote {
		emitLineC(cYellow, fmt.Sprintf("  ✏ OVERWROTE %s (%d bytes, backup at %s.bak)", path, len(content), path))
		hunks := diffLines(string(prevData), content)
		if d := renderDiff(hunks, useColor, 24); d != "" {
			emitDiff(strings.TrimRight(d, "\n"))
		}
		if d := renderDiff(hunks, false, 40); d != "" {
			overwriteDiff = "\nDiff vs the previous content:\n" + tail(d, 2048)
		}
	} else {
		emitLineC(cGreen, fmt.Sprintf("  ✏ created %s (%d bytes)", path, len(content)))
	}
	result := fmt.Sprintf("OK: wrote %d bytes to %s%s", len(content), rel, overwriteDiff)
	if out, ok := runHook("post_edit", map[string]string{"file": path}); !ok {
		result += "\nWARNING — post_edit hook failed:\n" + tail(out, 2048)
	}
	return result
}

func (s *Sandbox) toolEditFile(a toolArgs) string {
	rel := a.str("path")
	if isProtected(rel) {
		emitLineC(cRed, "  ✗ REJECTED edit of "+rel+" (protected path)")
		return "ERROR: " + rel + " matches a protected pattern (key material / user-configured). The agent may not modify it; ask the user to change it themselves if needed."
	}
	oldStr, newStr := a.str("old_str"), a.str("new_str")
	replaceAll, _ := a["replace_all"].(bool)
	if oldStr == "" {
		return "ERROR: old_str must not be empty. To create a new file, use write_file."
	}
	path, err := s.resolve(rel)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	content := string(data)
	n := strings.Count(content, oldStr)
	// Whitespace-forgiveness fallback: models frequently get indentation
	// subtly wrong. If the exact string isn't found, try matching with each
	// line's leading/trailing whitespace normalized. Only accept if the
	// relaxed match is unique — an ambiguous relaxed match is too risky to
	// apply blind.
	usedFuzzy := false
	if n == 0 {
		if fuzzyOld, ok := fuzzyFind(content, oldStr); ok {
			oldStr = fuzzyOld
			n = strings.Count(content, oldStr)
			usedFuzzy = true
		}
	}
	switch {
	case n == 0:
		emitLineC(cRed, fmt.Sprintf("  ✗ REJECTED edit of %s (old_str not found)", rel))
		return "ERROR: old_str not found in file. " + closestLines(string(data), oldStr) +
			"\nMatch the file's exact bytes (copy from the lines above), or rewrite the whole file with write_file."
	case n > 1 && !replaceAll:
		emitLineC(cRed, fmt.Sprintf("  ✗ REJECTED edit of %s (old_str appears %d times)", rel, n))
		return fmt.Sprintf("ERROR: old_str appears %d times; it must be unique. Include more surrounding context to disambiguate, or set replace_all=true to replace all %d occurrences.", n, n)
	}
	if _, err := s.backup(path); err != nil {
		return "ERROR: " + err.Error()
	}
	count := 1
	if replaceAll {
		count = n
	}
	updated := strings.Replace(content, oldStr, newStr, count)
	if err := writeAtomic(path, []byte(updated)); err != nil {
		return "ERROR: " + err.Error()
	}
	s.Modified = append(s.Modified, path)
	fuzzyNote := ""
	if usedFuzzy {
		fuzzyNote = " [whitespace-normalized match]"
	}
	emitLineC(cYellow, fmt.Sprintf("  ✏ EDITED %s (%d replacement(s), backup at %s.bak)%s", path, count, path, fuzzyNote))
	hunks := diffLines(content, updated)
	if d := renderDiff(hunks, useColor, 24); d != "" {
		emitDiff(strings.TrimRight(d, "\n"))
	}
	result := fmt.Sprintf("OK: replaced %d occurrence(s) in %s%s", count, rel, fuzzyNote)
	// The model gets the diff too: "OK" tells it nothing about whether the
	// edit landed where it thought — the hunks do.
	if d := renderDiff(hunks, false, 40); d != "" {
		result += "\nDiff of the change:\n" + tail(d, 2048)
	}
	if out, ok := runHook("post_edit", map[string]string{"file": path}); !ok {
		result += "\nWARNING — post_edit hook failed:\n" + tail(out, 2048)
	}
	return result
}

// closestLines is the difference between a model that fixes its match and a
// model that theorizes about "hidden characters" for three turns: when
// old_str isn't found, show the ACTUAL lines from the file that most
// resemble it, in Go %q syntax so every tab and space is visible. The real
// transcript failure this addresses: the file contained "Port     String"
// and the model searched for "Port     String," — a comma that was never
// there — five times, blind, because the error gave it nothing to compare
// against. Lines are ranked by longest-common-substring similarity, which
// keeps working when the model's mistake sits INSIDE the most distinctive
// token (exact-token search would find nothing, as in the comma case).
func closestLines(content, needle string) string {
	var probe string
	for _, nl := range strings.Split(needle, "\n") {
		if strings.TrimSpace(nl) != "" {
			probe = nl
			break
		}
	}
	if probe == "" {
		return "The old_str was empty or whitespace-only."
	}
	type scored struct {
		num  int
		line string
		s    int
	}
	var best []scored
	for i, l := range strings.Split(content, "\n") {
		if s := lcsLen(l, probe); s > 0 {
			best = append(best, scored{i + 1, l, s})
		}
	}
	sort.SliceStable(best, func(a, b int) bool { return best[a].s > best[b].s })
	// Require meaningful overlap — half the probe, capped so long probes
	// with a genuinely similar line still qualify.
	minScore := len(probe) / 2
	if minScore > 12 {
		minScore = 12
	}
	var hits []string
	for _, c := range best {
		if c.s < minScore || len(hits) == 3 {
			break
		}
		hits = append(hits, fmt.Sprintf("  line %d: %q", c.num, c.line))
	}
	if len(hits) == 0 {
		return "Nothing in the file resembles the old_str — re-read the file; it may have changed since you last saw it."
	}
	return fmt.Sprintf("Closest lines in the file (Go quoting: \\t is a tab):\n%s", strings.Join(hits, "\n"))
}

// lcsLen returns the length of the longest common substring of a and b.
func lcsLen(a, b string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	max := 0
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > max {
					max = cur[j]
				}
			} else {
				cur[j] = 0
			}
		}
		prev, cur = cur, prev
	}
	return max
}

// fuzzyFind looks for a block in content that matches want ignoring each
// line's leading/trailing whitespace. Returns the ACTUAL substring from
// content (so the caller can do a normal Replace) and whether the relaxed
// match was found and unique.
func fuzzyFind(content, want string) (string, bool) {
	wantLines := strings.Split(want, "\n")
	norm := func(s string) string { return strings.TrimSpace(s) }
	contentLines := strings.Split(content, "\n")
	var matchStart []int // starting line indices of candidate matches
	for i := 0; i+len(wantLines) <= len(contentLines); i++ {
		ok := true
		for j := range wantLines {
			if norm(contentLines[i+j]) != norm(wantLines[j]) {
				ok = false
				break
			}
		}
		if ok {
			matchStart = append(matchStart, i)
		}
	}
	if len(matchStart) != 1 {
		return "", false // not found, or ambiguous — don't guess
	}
	i := matchStart[0]
	actual := strings.Join(contentLines[i:i+len(wantLines)], "\n")
	return actual, true
}

func (s *Sandbox) toolListDir(a toolArgs) string {
	path, err := s.resolve(a.str("path"))
	if err != nil {
		return "ERROR: " + err.Error()
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			b.WriteString(e.Name() + "/\n")
		} else {
			b.WriteString(e.Name() + "\n")
		}
	}
	if b.Len() == 0 {
		return "(empty directory)"
	}
	return b.String()
}

func (s *Sandbox) toolGlob(a toolArgs) string {
	pattern := strings.TrimSpace(a.str("pattern"))
	if pattern == "" {
		return "ERROR: pattern must not be empty"
	}
	root, err := s.resolve(a.str("path"))
	if err != nil {
		return "ERROR: " + err.Error()
	}
	const maxMatches = 500
	skip := map[string]bool{".git": true, "node_modules": true, ".vscode": true, "vendor": true}
	var b strings.Builder
	matches := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || matches >= maxMatches {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if ok, _ := filepath.Match(pattern, d.Name()); ok {
			relp, _ := filepath.Rel(root, p)
			b.WriteString(relp + "\n")
			matches++
		}
		return nil
	})
	if matches == 0 {
		return "(no files match)"
	}
	if matches >= maxMatches {
		b.WriteString("...[match limit reached; narrow the pattern or path]\n")
	}
	return b.String()
}

func (s *Sandbox) toolSearchFiles(a toolArgs) string {
	re, err := regexp.Compile(a.str("pattern"))
	if err != nil {
		return "ERROR: invalid regex: " + err.Error()
	}
	root, err := s.resolve(a.str("path"))
	if err != nil {
		return "ERROR: " + err.Error()
	}
	const (
		maxMatches  = 200
		maxFileSize = 1 << 20 // skip files over 1MB
	)
	var b strings.Builder
	matches := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || matches >= maxMatches {
			return filepath.SkipAll
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".vscode":
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxFileSize {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(data[:min(len(data), 512)], 0) != -1 {
			return nil // unreadable or binary
		}
		relp, _ := filepath.Rel(root, p)
		for i, line := range strings.Split(string(data), "\n") {
			if matches >= maxMatches {
				break
			}
			if re.MatchString(line) {
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				fmt.Fprintf(&b, "%s:%d: %s\n", relp, i+1, strings.TrimSpace(line))
				matches++
			}
		}
		return nil
	})
	if matches == 0 {
		return "(no matches)"
	}
	if matches >= maxMatches {
		b.WriteString("...[match limit reached; narrow the pattern or path]\n")
	}
	return b.String()
}

func (s *Sandbox) toolTree(a toolArgs) string {
	root, err := s.resolve(a.str("path"))
	if err != nil {
		return "ERROR: " + err.Error()
	}
	maxDepth := a.num("depth")
	if maxDepth <= 0 {
		maxDepth = 3
	}
	skip := map[string]bool{".git": true, "node_modules": true, ".vscode": true, "vendor": true, "dist": true, "build": true}
	const maxEntries = 500
	var b strings.Builder
	count := 0
	var walk func(dir, prefix string, depth int) bool // returns false if truncated
	walk = func(dir, prefix string, depth int) bool {
		if depth > maxDepth {
			return true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		// dirs first, then files, each alphabetical (ReadDir already sorts)
		var dirs, files []os.DirEntry
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e)
			} else {
				files = append(files, e)
			}
		}
		ordered := append(dirs, files...)
		for i, e := range ordered {
			if count >= maxEntries {
				b.WriteString(prefix + "…[tree truncated at 500 entries]\n")
				return false
			}
			last := i == len(ordered)-1
			branch, cont := "├── ", "│   "
			if last {
				branch, cont = "└── ", "    "
			}
			name := e.Name()
			if e.IsDir() {
				if skip[name] {
					b.WriteString(prefix + branch + name + "/ …(skipped)\n")
					count++
					continue
				}
				b.WriteString(prefix + branch + name + "/\n")
				count++
				if !walk(filepath.Join(dir, name), prefix+cont, depth+1) {
					return false
				}
			} else {
				b.WriteString(prefix + branch + name + "\n")
				count++
			}
		}
		return true
	}
	b.WriteString(root + "\n")
	walk(root, "", 1)
	return b.String()
}

func (s *Sandbox) toolFetchURL(a toolArgs) string {
	raw := strings.TrimSpace(a.str("url"))
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "ERROR: url must start with http:// or https://"
	}
	// Human in the loop: fetching hits the network, so always confirm
	// (except in non-interactive -yes mode, where there's no one to ask).
	if assumeYes {
		fmt.Println(tint(cDim, "  🌐 "+raw+" (auto-approved: -yes)"))
	} else {
		fmt.Println(tint(cCyan, "  🌐 "+raw))
		notifyApproval("fetch " + raw)
		if approvals.request("    fetch this URL? [y/N] ", "fetch") == approveDeny {
			fmt.Println("    (declined)")
			return "User declined to fetch this URL."
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	req.Header.Set("User-Agent", "agent-cli/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	const limit = 100 * 1024
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	text := string(body)
	truncated := ""
	if len(text) > limit {
		text = text[:limit]
		truncated = "\n...[truncated at 100KB]"
	}
	fmt.Println(tint(cDim, fmt.Sprintf("    (fetched %d bytes, HTTP %d)", len(body), resp.StatusCode)))
	return fmt.Sprintf("HTTP %d, %s\n\n%s%s", resp.StatusCode, resp.Header.Get("Content-Type"), text, truncated)
}

// execShell runs a command via bash in dir with the shared timeout, killing
// the WHOLE process group on timeout — exec.CommandContext alone kills only
// bash itself, leaving grandchildren (a hung test binary, a dev server)
// running orphaned. With live=true, output streams to the terminal (dimmed)
// as it happens — no dead air during a two-minute build — while still being
// captured in full for the tool result. Returns combined output and the exit
// code; exitCode is -1 for timeouts and other non-exit errors. (Setpgid is
// Linux/WSL2-only, which is where this agent lives.)
func ExecShell(cmdStr, dir string, live bool) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", cmdStr)
	cmd.Dir = dir
	setProcessGroup(cmd)            // platform-specific: kill the whole process tree on cancel
	cmd.WaitDelay = 5 * time.Second // don't block forever on inherited pipes
	var buf bytes.Buffer
	if live && useColor {
		fmt.Print("\033[2m") // whole stream dim; reset below
		w := io.MultiWriter(&buf, os.Stdout)
		cmd.Stdout, cmd.Stderr = w, w
	} else if live {
		w := io.MultiWriter(&buf, os.Stdout)
		cmd.Stdout, cmd.Stderr = w, w
	} else {
		cmd.Stdout, cmd.Stderr = &buf, &buf
	}
	err := cmd.Run()
	if live && useColor {
		fmt.Print("\033[0m")
	}
	// Large cap: build/test output can be big, and with a 262k-context model
	// the model can handle it. When output IS truncated, keep the TAIL rather
	// than the head — the actual error and summary line usually come last.
	const limit = 100 * 1024
	result := buf.String()
	if len(result) > limit {
		result = "...[earlier output truncated; showing last 100KB]\n" + result[len(result)-limit:]
	}
	if err != nil {
		// Order matters: a timed-out command also surfaces as an ExitError
		// (the group was SIGKILLed), so the context check must come first.
		if ctx.Err() == context.DeadlineExceeded {
			return result, -1, fmt.Errorf("command timed out after %v and was killed (with its process group)", commandTimeout)
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return result, ee.ExitCode(), nil
		}
		return result, -1, err
	}
	return result, 0, nil
}

func (s *Sandbox) toolRunCommand(a toolArgs) string {
	cmdStr := strings.TrimSpace(a.str("command"))
	if cmdStr == "" {
		return "ERROR: empty command"
	}
	// Human in the loop: mutating commands need explicit approval.
	// Read-only commands on the allowlist run without the prompt.
	if builtinAutoApproved(cmdStr) {
		fmt.Println(tint(cDim, fmt.Sprintf("  $ %s (auto-approved: read-only)", cmdStr)))
	} else if autoApproved(cmdStr) {
		fmt.Println(tint(cDim, fmt.Sprintf("  $ %s (auto-approved: on your allowlist)", cmdStr)))
	} else if assumeYes {
		fmt.Println(tint(cDim, fmt.Sprintf("  $ %s (auto-approved: -yes)", cmdStr)))
	} else {
		tok := strings.Fields(cmdStr)[0]
		fmt.Println(tint(cCyan, "  $ "+cmdStr))
		notifyApproval("run: " + cmdStr)
		prompt := fmt.Sprintf("    run this in %s? [y/N/a=always allow %q] ", s.Root, tok)
		switch approvals.request(prompt, tok) {
		case approveOnce:
			// approved for this run only
		case approveAlways:
			if msg := appendAllow(tok + " "); msg != "" {
				fmt.Println("    (allowlist error: " + msg + " — running once anyway)")
			} else {
				fmt.Printf("    (added %q to the allowlist — future %s commands run without asking)\n", tok+" ", tok)
			}
		default:
			fmt.Println("    (declined)")
			return "User declined to run this command. Do not retry it; ask the user what to do instead if needed."
		}
	}
	if out, ok := runHook("pre_command", map[string]string{"cmd": cmdStr}); !ok {
		fmt.Println(tint(cYellow, "  ✗ blocked by pre_command hook"))
		return "ERROR: blocked by the user's pre_command hook:\n" + tail(out, 2048)
	}
	result, exitCode, err := execShell(cmdStr, s.Root, true)
	if err != nil {
		return fmt.Sprintf("ERROR: %v\n%s", err, result)
	}
	return fmt.Sprintf("exit code: %d\n%s", exitCode, result)
}

// Summary prints every file modified this session, deduplicated in order.
func (s *Sandbox) Summary() {
	if len(s.Modified) == 0 {
		fmt.Println("\nNo files were modified this session.")
		return
	}
	fmt.Println("\nFiles modified this session:")
	seen := map[string]bool{}
	for _, p := range s.Modified {
		if !seen[p] {
			seen[p] = true
			fmt.Println("  " + p)
		}
	}
	fmt.Println("(overwritten/edited files have a .bak backup alongside them)")
}
