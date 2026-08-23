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
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------- Atomic multi-file edits ----------

func toolEditFiles(s *Sandbox, a toolArgs) string {
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
		if isProtected(e.Path) {
			return fmt.Sprintf("ERROR: edit %d targets protected path %s — nothing applied", i+1, e.Path)
		}
		abs, err := s.resolve(e.Path)
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
				i+1, e.Path, closestLines(string(data), e.OldStr))
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
		if _, err := s.backup(abs); err != nil {
			return "ERROR: " + err.Error() + fmt.Sprintf(" (some of %d files may be unwritten — check /diff)", len(order))
		}
	}
	for _, abs := range order {
		if err := writeAtomic(abs, working[abs]); err != nil {
			return "ERROR: write failed for " + abs + ": " + err.Error() + " — check /diff for partial state"
		}
		s.Modified = append(s.Modified, abs)
		rel, _ := filepath.Rel(s.Root, abs)
		fmt.Println(tint(cYellow, "  ✏ EDITED "+rel+" (atomic set)"))
		if out, ok := runHook("post_edit", map[string]string{"file": abs}); !ok {
			fmt.Fprintf(&b, "post_edit hook failed for %s:\n%s\n", rel, tail(out, 512))
		}
	}
	result := fmt.Sprintf("OK: applied %d edit(s) across %d file(s) atomically: %s",
		len(edits), len(order), strings.Join(relPaths(s.Root, order), ", "))
	if b.Len() > 0 {
		result += "\nWARNINGS:\n" + b.String()
	}
	return result
}

func relPaths(root string, abs []string) []string {
	out := make([]string, len(abs))
	for i, p := range abs {
		r, _ := filepath.Rel(root, p)
		out[i] = r
	}
	return out
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

func toolRenameSymbol(s *Sandbox, a toolArgs) string {
	if !goplsAvailable() {
		return "ERROR: gopls is not installed (go install golang.org/x/tools/gopls@latest). " +
			"Fall back to edit_files: change the declaration and every caller in one atomic set."
	}
	file := strings.TrimSpace(a.str("file"))
	sym := strings.TrimSpace(a.str("symbol"))
	newName := strings.TrimSpace(a.str("new_name"))
	if file == "" || sym == "" || newName == "" {
		return "ERROR: file, symbol, and new_name are all required"
	}
	abs, err := s.resolve(file)
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
			err, tail(string(out), 1024))
	}
	// gopls edited files behind our back; they're now modified this session.
	// Mark the whole workspace dirty conservatively so /diff and verify see
	// it. (We can't know exactly which files gopls touched without -d
	// parsing; the git checkpoint taken before the turn is the safety net.)
	fmt.Println(tint(cYellow, fmt.Sprintf("  ✏ RENAMED %s → %s (gopls, workspace-wide)", sym, newName)))
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = "renamed across the workspace"
	}
	return "OK: " + msg + " — run go_diagnostics or the verify command to confirm the build."
}

func toolGoDiagnostics(s *Sandbox, a toolArgs) string {
	if !goplsAvailable() {
		return "ERROR: gopls is not installed — run the verify command (go build/test) instead."
	}
	target := strings.TrimSpace(a.str("path"))
	abs := s.Root
	if target != "" {
		if r, err := s.resolve(target); err == nil {
			abs = r
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gopls", "check", abs)
	cmd.Dir = s.Root
	out, _ := cmd.CombinedOutput() // nonzero exit is normal when diagnostics exist
	res := strings.TrimSpace(string(out))
	if res == "" {
		return "OK: gopls reports no diagnostics."
	}
	return "Diagnostics from gopls (type errors and vet findings, no build run):\n" + tail(res, 4096)
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

var (
	reTestFail    = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	reAssertLine  = regexp.MustCompile(`^\s*([\w./-]+\.go):(\d+):`)
	rePanicLine   = regexp.MustCompile(`^panic:`)
	reBuildFailGo = regexp.MustCompile(`^(# .+|.+\.go:\d+:\d+:)`)
)

// parseTestFailures distills `go test` (or build) output to the actionable
// parts: which tests failed and the file:line + message of each assertion.
// Returns "" when nothing failed (caller keeps the raw output for context).
func parseTestFailures(out string) string {
	lines := strings.Split(out, "\n")
	var b strings.Builder
	failedTests := []string{}
	assertions := []string{}
	inFail := false
	for i, ln := range lines {
		if m := reTestFail.FindStringSubmatch(ln); m != nil {
			failedTests = append(failedTests, m[1])
			inFail = true
			continue
		}
		if strings.HasPrefix(ln, "=== RUN") || strings.HasPrefix(ln, "--- PASS") || strings.HasPrefix(ln, "ok ") {
			inFail = false
		}
		if inFail {
			if m := reAssertLine.FindStringSubmatch(ln); m != nil {
				msg := strings.TrimSpace(ln)
				// Pull a continuation line only if it's not itself a new
				// assertion, a test-status marker, or a summary line.
				if i+1 < len(lines) {
					nxt := strings.TrimSpace(lines[i+1])
					isNoise := nxt == "" || nxt == "FAIL" || nxt == "PASS" ||
						strings.HasPrefix(nxt, "---") || strings.HasPrefix(nxt, "===") ||
						strings.HasPrefix(nxt, "FAIL\t") || strings.HasPrefix(nxt, "ok ") ||
						reAssertLine.MatchString(lines[i+1])
					if !isNoise {
						msg += " " + nxt
					}
				}
				assertions = append(assertions, msg)
			}
		}
		if rePanicLine.MatchString(ln) {
			assertions = append(assertions, strings.TrimSpace(ln))
		}
	}
	// Build errors (no test framing): surface the compiler lines.
	if len(failedTests) == 0 {
		var buildErrs []string
		for _, ln := range lines {
			if reBuildFailGo.MatchString(ln) {
				buildErrs = append(buildErrs, strings.TrimSpace(ln))
			}
		}
		if len(buildErrs) == 0 {
			return ""
		}
		if len(buildErrs) > 12 {
			buildErrs = buildErrs[:12]
		}
		return "Build errors:\n" + strings.Join(buildErrs, "\n")
	}
	sort.Strings(failedTests)
	b.WriteString(fmt.Sprintf("%d test(s) failed: %s\n", len(failedTests), strings.Join(dedup(failedTests), ", ")))
	if len(assertions) > 0 {
		if len(assertions) > 15 {
			assertions = assertions[:15]
		}
		b.WriteString("Failing assertions:\n")
		for _, a := range assertions {
			b.WriteString("  " + a + "\n")
		}
	}
	return b.String()
}

func dedup(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// registerStructuredTools adds the atomic/gopls tools. gopls tools are
// registered even when gopls is absent — they return an install hint rather
// than vanishing, so the model learns the capability exists.
func registerStructuredTools() {
	registerTools(
		Tool{
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
		Tool{
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
		Tool{
			Name: "go_diagnostics",
			Desc: "Get Go type errors and vet findings from gopls WITHOUT running a build — a fast way to check whether the code compiles cleanly after an edit. Optionally scope to a file or package path.",
			Props: map[string]any{
				"path": map[string]any{"type": "string", "description": "Optional file or directory to check (default: whole workdir)"},
			},
			Handler: toolGoDiagnostics,
		},
	)
	readOnlyTools["go_diagnostics"] = true
	buildToolSchemas()
}
