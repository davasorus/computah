// Semantic git tools — structured, read-only access to repository history so
// the model can understand code provenance without shelling out through
// run_command and parsing raw `git` text itself.
//
// Three tools:
//
//	git_log   — recent commits (hash, author, date, subject), optionally for a path
//	git_blame — who last changed each line of a file (or a line range)
//	git_show  — a single commit's message + diff
//
// All are read-only (no working-tree mutation), so they're marked read-only:
// available in plan mode and safe to batch. They use `git --no-pager` with
// stable formatting and confine any path argument to the workspace via the
// sandbox, matching the file tools' safety model.
package toolsext

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
)

// gitField separators for a stable, easy-to-parse log format. Using unit/record
// separators avoids collisions with anything in a commit subject.
const (
	gitFieldSep  = "\x1f" // between fields of one commit
	gitRecordSep = "\x1e" // between commits
)

// runGit executes a read-only git command in the sandbox root and returns its
// combined output. It uses a direct argv vector (agent.ExecArgv) — NOT a
// shell — so a git ref or path containing shell metacharacters (;, |, `,
// $(), &) is passed to git verbatim and cannot inject a command. `--no-pager`
// prevents git from trying to invoke a pager in a non-interactive context.
func runGit(s *agent.Sandbox, args ...string) (string, int, error) {
	full := append([]string{"--no-pager"}, args...)
	return agent.ExecArgv(s.Root, "git", full...)
}

func toolGitLog(s *agent.Sandbox, a agent.ToolArgs) string {
	n := a.Num("count")
	if n <= 0 {
		n = 15
	}
	if n > 100 {
		n = 100
	}
	// %h short hash, %an author, %ad date (short), %s subject. No shell
	// quoting — ExecArgv passes this as one argv element verbatim.
	format := strings.Join([]string{"%h", "%an", "%ad", "%s"}, gitFieldSep) + gitRecordSep
	args := []string{"log", "--date=short", fmt.Sprintf("--max-count=%d", n),
		"--pretty=format:" + format}

	// Optional path scope: confine it to the workspace first.
	if p := strings.TrimSpace(a.Str("path")); p != "" {
		abs, err := core.ConfinePath(s.Root, p)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		args = append(args, "--", abs)
	}

	out, code, err := runGit(s, args...)
	if err != nil {
		return "ERROR running git log: " + err.Error() + "\n" + agent.Tail(out, 1024)
	}
	if code != 0 {
		return "git log failed (exit " + strconv.Itoa(code) + "):\n" + agent.Tail(out, 2048)
	}
	return formatGitLog(out)
}

// formatGitLog turns the separator-delimited log into an aligned, readable
// table the model can scan: "hash  date  author  subject".
func formatGitLog(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "No commits (empty repository or no history for that path)."
	}
	var b strings.Builder
	for _, rec := range strings.Split(raw, gitRecordSep) {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		f := strings.Split(rec, gitFieldSep)
		if len(f) < 4 {
			continue
		}
		fmt.Fprintf(&b, "%s  %s  %s  %s\n", f[0], f[2], f[1], f[3])
	}
	s := b.String()
	if s == "" {
		return "No commits found."
	}
	return "Recent commits (hash  date  author  subject):\n" + s
}

func toolGitBlame(s *agent.Sandbox, a agent.ToolArgs) string {
	file := strings.TrimSpace(a.Str("file"))
	if file == "" {
		return "ERROR: file is required"
	}
	abs, err := core.ConfinePath(s.Root, file)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	args := []string{"blame", "--date=short"}
	// Optional line range: start_line / end_line map to git blame -L a,b.
	start, end := a.Num("start_line"), a.Num("end_line")
	if start > 0 {
		if end <= 0 || end < start {
			end = start
		}
		// -L<start>,<end> as ONE argv token (no shell to split "-L 1,5").
		args = append(args, fmt.Sprintf("-L%d,%d", start, end))
	}
	args = append(args, "--", abs)

	out, code, err := runGit(s, args...)
	if err != nil {
		return "ERROR running git blame: " + err.Error() + "\n" + agent.Tail(out, 1024)
	}
	if code != 0 {
		return "git blame failed (exit " + strconv.Itoa(code) + "):\n" + agent.Tail(out, 2048)
	}
	res := strings.TrimSpace(out)
	if res == "" {
		return "No blame output (file may be untracked or empty)."
	}
	return "git blame for " + file + ":\n" + agent.Tail(res, 4096)
}

func toolGitShow(s *agent.Sandbox, a agent.ToolArgs) string {
	ref := strings.TrimSpace(a.Str("ref"))
	if ref == "" {
		ref = "HEAD"
	}
	// Guard against a ref that's actually flags/options (defense against the
	// model passing something that starts with '-'); a legitimate ref never
	// begins with a dash.
	if strings.HasPrefix(ref, "-") {
		return "ERROR: invalid ref (must not start with '-')"
	}
	// --stat gives a change summary before the diff; cap the diff so a huge
	// commit doesn't blow the context.
	out, code, err := runGit(s, "show", "--stat", "--patch", ref)
	if err != nil {
		return "ERROR running git show: " + err.Error() + "\n" + agent.Tail(out, 1024)
	}
	if code != 0 {
		return "git show failed (exit " + strconv.Itoa(code) + "):\n" + agent.Tail(out, 2048)
	}
	res := strings.TrimSpace(out)
	if res == "" {
		return "No output for ref " + ref + "."
	}
	return "git show " + ref + ":\n" + agent.Tail(res, 6144)
}

// RegisterGitTools adds the read-only semantic git tools to the engine.
func RegisterGitTools() {
	agent.RegisterTools(
		agent.Tool{
			Name: "git_log",
			Desc: "Show recent commit history as a structured list (hash, date, author, subject). Use to understand how code evolved or find when something changed. Optionally scope to a file or directory path, and set count (default 15, max 100).",
			Props: map[string]any{
				"count": map[string]any{"type": "integer", "description": "How many commits to show (default 15, max 100)"},
				"path":  map[string]any{"type": "string", "description": "Optional file or directory to scope history to"},
			},
			Handler: toolGitLog,
		},
		agent.Tool{
			Name: "git_blame",
			Desc: "Show who last changed each line of a file, with commit and date — use to find the origin of a specific line or block before editing it. Optionally scope to a line range with start_line/end_line.",
			Props: map[string]any{
				"file":       map[string]any{"type": "string", "description": "Path to the file to blame"},
				"start_line": map[string]any{"type": "integer", "description": "First line of an optional range"},
				"end_line":   map[string]any{"type": "integer", "description": "Last line of an optional range"},
			},
			Required: []string{"file"},
			Handler:  toolGitBlame,
		},
		agent.Tool{
			Name: "git_show",
			Desc: "Show a single commit's summary and diff (message, changed-file stats, and patch). Use to inspect exactly what a commit changed. ref defaults to HEAD; accepts any commit-ish (hash, tag, HEAD~2).",
			Props: map[string]any{
				"ref": map[string]any{"type": "string", "description": "Commit-ish to show (default HEAD)"},
			},
			Handler: toolGitShow,
		},
	)
	// All three are read-only: no working-tree mutation, safe in plan mode and
	// safe to batch concurrently.
	agent.MarkReadOnly("git_log")
	agent.MarkReadOnly("git_blame")
	agent.MarkReadOnly("git_show")
	agent.RebuildToolSchemas()
}
