// Structured & atomic editing — the "safe refactor" toolset.
//
// Three capabilities the plain edit_file loop couldn't give a small model:
//
//	edit_files   — N edits across M files applied ATOMICALLY: every edit
//	               validates first, then all apply, or none do. The DI
//	               refactor failed repeatedly because the model changed
//	               todo.go, broke the build, and couldn't change cmd/*.go
//	               in the same breath — the tree was red between turns.
//	               This makes "change the signature and every caller" one
//	               indivisible step.
//
//	rename_symbol— a REAL rename via gopls, not text substitution: renames
//	               the declaration and every reference across packages,
//	               respecting scope (won't touch a same-named field on an
//	               unrelated type). Falls back with a clear message when
//	               gopls isn't installed.
//
//	go_diagnostics—type errors and vet findings from gopls WITHOUT a build,
//	               so the model can check its work cheaply mid-turn.
//
// Plus parseTestFailures (used by the verify loop): turns a wall of `go
// test` output into just the failing tests and their file:line assertions,
// so the model gets a target instead of a haystack.
package toolsext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
)

// ---------- Atomic multi-file edits ----------

func toolEditFiles(s *agent.Sandbox, a agent.ToolArgs) string {
	raw, err := json.Marshal(a["edits"])
	if err != nil {
		return "ERROR: bad edits payload"
	}
	var edits []struct {
		Path   string `json:"path"`
		OldStr string `json:"old_str"`
		NewStr string `json:"new_str"`
	}
	if err := json.Unmarshal(raw, &edits); err != nil {
		return "ERROR: edits must be an array of {path, old_str, new_str}"
	}
	if len(edits) == 0 {
		return "ERROR: no edits provided"
	}

	// PASS 1 — validate everything against a working copy in memory. Group
	// edits by file so multiple edits to one file compose in order, and so
	// each file's final content is computed before anything touches disk.
	working := map[string][]byte{} // resolved path -> current (edited) bytes
	order := []string{}            // resolved paths, first-seen order
	for i, e := range edits {
		if e.Path == "" {
			return fmt.Sprintf("ERROR: edit %d has no path — nothing applied", i+1)
		}
		if agent.IsProtected(e.Path) {
			return fmt.Sprintf("ERROR: edit %d targets protected path %s — nothing applied", i+1, e.Path)
		}
		abs, err := s.Resolve(e.Path)
		if err != nil {
			return fmt.Sprintf("ERROR: edit %d: %v — nothing applied", i+1, err)
		}
		data, ok := working[abs]
		if !ok {
			d, err := os.ReadFile(abs)
			if err != nil {
				return fmt.Sprintf("ERROR: edit %d: cannot read %s: %v — nothing applied", i+1, e.Path, err)
			}
			data = d
			working[abs] = d
			order = append(order, abs)
		}
		if e.OldStr == "" {
			return fmt.Sprintf("ERROR: edit %d (%s): old_str is empty — nothing applied", i+1, e.Path)
		}
		n := strings.Count(string(data), e.OldStr)
		if n == 0 {
			return fmt.Sprintf("ERROR: edit %d (%s): old_str not found. %s — nothing applied.",
				i+1, e.Path, agent.ClosestLines(string(data), e.OldStr))
		}
		if n > 1 {
			return fmt.Sprintf("ERROR: edit %d (%s): old_str appears %d times — make it unique. Nothing applied.", i+1, e.Path, n)
		}
		working[abs] = []byte(strings.Replace(string(data), e.OldStr, e.NewStr, 1))
	}

	// PASS 2 — everything validated; commit. Backups first (pre-session
	// originals), then atomic writes.
	var b strings.Builder
	for _, abs := range order {
		if _, err := s.Backup(abs); err != nil {
			return "ERROR: " + err.Error() + fmt.Sprintf(" (some of %d files may be unwritten — check /diff)", len(order))
		}
	}
	for _, abs := range order {
		if err := agent.WriteAtomic(abs, working[abs]); err != nil {
			return "ERROR: write failed for " + abs + ": " + err.Error() + " — check /diff for partial state"
		}
		s.Modified = append(s.Modified, abs)
		rel, _ := filepath.Rel(s.Root, abs)
		fmt.Println(core.Tint(core.ColorYellow, "  ✏ EDITED "+core.LogSafe(rel)+" (atomic set)"))
		if out, ok := agent.RunHook("post_edit", map[string]string{"file": abs}); !ok {
			fmt.Fprintf(&b, "post_edit hook failed for %s:\n%s\n", rel, agent.Tail(out, 512))
		}
	}
	result := fmt.Sprintf("OK: applied %d edit(s) across %d file(s) atomically: %s",
		len(edits), len(order), strings.Join(core.RelPaths(s.Root, order), ", "))
	if b.Len() > 0 {
		result += "\nWARNINGS:\n" + b.String()
	}
	return result
}

// ---------- gopls: rename_symbol and go_diagnostics ----------

var goplsChecked, goplsAvail bool

func goplsAvailable() bool {
	if !goplsChecked {
		goplsChecked = true
		_, err := exec.LookPath("gopls")
		goplsAvail = err == nil
	}
	return goplsAvail
}

func toolRenameSymbol(s *agent.Sandbox, a agent.ToolArgs) string {
	if !goplsAvailable() {
		return "ERROR: gopls is not installed (go install golang.org/x/tools/gopls@latest). " +
			"Fall back to edit_files: change the declaration and every caller in one atomic set."
	}
	file := strings.TrimSpace(a.Str("file"))
	sym := strings.TrimSpace(a.Str("symbol"))
	newName := strings.TrimSpace(a.Str("new_name"))
	if file == "" || sym == "" || newName == "" {
		return "ERROR: file, symbol, and new_name are all required"
	}
	abs, err := s.Resolve(file)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "ERROR: cannot read " + file + ": " + err.Error()
	}
	// gopls rename addresses the symbol by byte offset. Find the first
	// standalone occurrence of the identifier.
	off := findIdentOffset(string(data), sym)
	if off < 0 {
		return fmt.Sprintf("ERROR: identifier %q not found in %s — open the file and rename by edit_files instead", sym, file)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// -w writes changes across the workspace; -d would show a diff instead.
	cmd := exec.CommandContext(ctx, "gopls", "rename", "-w",
		fmt.Sprintf("%s:#%d", abs, off), newName)
	cmd.Dir = s.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("ERROR: gopls rename failed: %s\n%s\nFall back to edit_files if this symbol is tricky.",
			err, agent.Tail(string(out), 1024))
	}
	// gopls edited files behind our back; they're now modified this session.
	// Mark the whole workspace dirty conservatively so /diff and verify see
	// it. (We can't know exactly which files gopls touched without -d
	// parsing; the git checkpoint taken before the turn is the safety net.)
	fmt.Println(core.Tint(core.ColorYellow, fmt.Sprintf("  ✏ RENAMED %s → %s (gopls, workspace-wide)", core.LogSafe(sym), core.LogSafe(newName))))
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = "renamed across the workspace"
	}
	return "OK: " + msg + " — run go_diagnostics or the verify command to confirm the build."
}

func toolGoDiagnostics(s *agent.Sandbox, a agent.ToolArgs) string {
	target := strings.TrimSpace(a.Str("path"))
	abs := s.Root
	if target != "" {
		if r, err := s.Resolve(target); err == nil {
			abs = r
		}
	}
	// Preferred path: gopls check — type errors and vet findings WITHOUT a
	// build, so it's fast and catches type errors a plain build would too.
	if goplsAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gopls", "check", abs)
		cmd.Dir = s.Root
		out, _ := cmd.CombinedOutput() // nonzero exit is normal when diagnostics exist
		res := strings.TrimSpace(string(out))
		if res == "" {
			return "OK: gopls reports no diagnostics."
		}
		return "Diagnostics from gopls (type errors and vet findings, no build run):\n" + agent.Tail(res, 4096)
	}
	// Fallback: no gopls, so use the toolchain directly. `go vet` surfaces
	// both compile errors and vet findings; parse its output into clean
	// file:line diagnostics the model can act on without re-reading raw text.
	return goVetDiagnostics(s, abs)
}

// goVetDiagnostics runs `go vet` (which compiles first, so it reports build
// errors too) on the target and returns structured diagnostics. This is the
// gopls-free fallback so go_diagnostics works on any machine with the Go
// toolchain, not only where gopls is installed.
func goVetDiagnostics(s *agent.Sandbox, abs string) string {
	// Resolve the target to a package pattern relative to the module root so
	// `go vet` scopes correctly: a directory becomes "./rel/...", the root
	// becomes "./...".
	pattern := "./..."
	if rel, err := filepath.Rel(s.Root, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		pattern = "./" + filepath.ToSlash(rel) + "/..."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "vet", pattern)
	cmd.Dir = s.Root
	out, err := cmd.CombinedOutput() // nonzero exit is normal when problems exist
	if ctx.Err() == context.DeadlineExceeded {
		return "ERROR: go vet timed out after 120s — the package may be very large; narrow with a path argument."
	}
	res := strings.TrimSpace(string(out))
	if res == "" && err == nil {
		return "OK: go vet reports no build errors or vet findings. (gopls not installed; install it for type-level diagnostics without a build.)"
	}
	diags := parseGoVet(res, s.Root)
	if len(diags) == 0 {
		// Output that didn't parse as file:line diagnostics (e.g. a module
		// resolution error) is still worth returning verbatim.
		return "go vet output (gopls not installed):\n" + agent.Tail(res, 4096)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Diagnostics from `go vet` (%d; gopls not installed — these include build errors):\n", len(diags))
	for _, d := range diags {
		b.WriteString("  " + d + "\n")
	}
	return agent.Tail(b.String(), 4096)
}

// parseGoVet extracts "file:line[:col]: message" diagnostics from go vet /
// compiler output, normalizing absolute paths back to workspace-relative so
// the model sees the same paths it uses for edits. Non-diagnostic noise
// (the leading "# pkg" headers, "go: ..." lines) is dropped.
func parseGoVet(out, root string) []string {
	// file:line: msg  or  file:line:col: msg
	re := regexp.MustCompile(`^(.+?\.go):(\d+)(?::(\d+))?:\s*(.*)$`)
	var diags []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "go:") {
			continue
		}
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if rel, err := filepath.Rel(root, file); err == nil && !strings.HasPrefix(rel, "..") {
			file = filepath.ToSlash(rel)
		}
		loc := file + ":" + m[2]
		if m[3] != "" {
			loc += ":" + m[3]
		}
		diags = append(diags, loc+": "+m[4])
	}
	return diags
}

// toolRunTests runs the Go test suite (optionally scoped to a package path)
// and returns DISTILLED failures — the failing test names plus each
// assertion's file:line and message — the same actionable summary the verify
// loop feeds back after a file-modifying turn, but callable ON DEMAND so the
// model can check its work proactively instead of waiting for verify. On a
// clean run it says so; on failure it leads with the parsed failures and
// appends a raw tail for context.
func toolRunTests(s *agent.Sandbox, a agent.ToolArgs) string {
	pattern := testPattern(a.Str("path"))
	cmd := "go test " + pattern
	out, code, err := agent.ExecShell(cmd, s.Root, false)
	if err != nil {
		return "ERROR running tests: " + err.Error() + "\n" + agent.Tail(out, 2048)
	}
	if code == 0 {
		return "OK: `" + cmd + "` passed — no test failures."
	}
	// Non-zero exit: distill the failures for the model.
	feedback := fmt.Sprintf("`%s` failed (exit %d).", cmd, code)
	if parsed := core.ParseTestFailures(out); parsed != "" {
		feedback += "\n" + parsed + "\nFull output tail:\n" + agent.Tail(out, 2048)
	} else {
		feedback += " Output tail:\n" + agent.Tail(out, 4096)
	}
	return feedback
}

// testPattern normalizes a user/model-supplied path into a `go test` package
// pattern: empty → the whole module (./...), a bare relative dir → rooted at
// the workdir (./dir), an already-qualified pattern passes through.
func testPattern(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "./..."
	}
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "/") {
		return p
	}
	return "./" + p
}

// findIdentOffset returns the byte offset of the first occurrence of ident
// that stands alone (not a substring of a longer identifier).
func findIdentOffset(src, ident string) int {
	isWord := func(b byte) bool {
		return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	from := 0
	for {
		i := strings.Index(src[from:], ident)
		if i < 0 {
			return -1
		}
		pos := from + i
		leftOK := pos == 0 || !isWord(src[pos-1])
		rightPos := pos + len(ident)
		rightOK := rightPos >= len(src) || !isWord(src[rightPos])
		if leftOK && rightOK {
			return pos
		}
		from = pos + len(ident)
	}
}

// ---------- Test-failure parsing ----------

// RegisterStructuredTools adds the atomic/gopls tools. gopls tools are
// registered even when gopls is absent — they return an install hint rather
// than vanishing, so the model learns the capability exists.
func RegisterStructuredTools() {
	agent.RegisterTools(
		agent.Tool{
			Name: "edit_files",
			Desc: "Apply multiple edits across one or more files ATOMICALLY — all succeed or none apply. Use this whenever a change spans files that must stay consistent (rename a function and its callers, change a signature and its call sites) so the build is never broken between edits. Each edit is {path, old_str, new_str} with a unique old_str, same matching rules as edit_file.",
			Props: map[string]any{
				"edits": map[string]any{
					"type":        "array",
					"description": "The complete set of edits to apply together",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path":    map[string]any{"type": "string"},
							"old_str": map[string]any{"type": "string", "description": "Exact text to replace; must be unique within its file"},
							"new_str": map[string]any{"type": "string"},
						},
						"required": []string{"path", "old_str", "new_str"},
					},
				},
			},
			Required: []string{"edits"},
			Handler:  toolEditFiles,
		},
		agent.Tool{
			Name: "rename_symbol",
			Desc: "Rename a Go symbol (function, type, variable, field) and every reference to it across the whole module, scope-aware, via gopls. Prefer this over manual editing for renames — it won't miss a caller or touch an unrelated same-named identifier. Give the file where the symbol is defined.",
			Props: map[string]any{
				"file":     map[string]any{"type": "string", "description": "A file where the symbol appears (usually its definition)"},
				"symbol":   map[string]any{"type": "string", "description": "Current identifier name"},
				"new_name": map[string]any{"type": "string", "description": "New identifier name"},
			},
			Required: []string{"file", "symbol", "new_name"},
			Handler:  toolRenameSymbol,
		},
		agent.Tool{
			Name: "go_diagnostics",
			Desc: "Get Go type errors and vet findings — a fast way to check whether the code compiles cleanly after an edit. Uses gopls when available (type errors without a build); otherwise falls back to `go vet` (which also reports build errors), so it works with just the Go toolchain. Optionally scope to a file or package path.",
			Props: map[string]any{
				"path": map[string]any{"type": "string", "description": "Optional file or directory to check (default: whole workdir)"},
			},
			Handler: toolGoDiagnostics,
		},
		agent.Tool{
			Name: "run_tests",
			Desc: "Run the Go test suite and get DISTILLED failures — the failing test names plus each assertion's file:line and message — instead of a wall of `go test` output. Use it to check your work proactively after edits. Optionally scope to a package path (e.g. \"internal/agent\"); defaults to the whole module (./...).",
			Props: map[string]any{
				"path": map[string]any{"type": "string", "description": "Optional package path to test (default: ./... — the whole module)"},
			},
			Handler: toolRunTests,
		},
	)
	agent.MarkReadOnly("go_diagnostics")
	agent.RebuildToolSchemas()
}
