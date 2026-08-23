package toolsext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davasorus/computah/internal/agent"
)

func TestFormatGitLog(t *testing.T) {
	// Simulate the --pretty=format output: fields joined by gitFieldSep,
	// records by gitRecordSep, wrapped in the quotes git emits.
	rec := func(h, author, date, subj string) string {
		return strings.Join([]string{h, author, date, subj}, gitFieldSep)
	}
	raw := "'" + rec("abc123", "Sean", "2026-08-23", "feat: add git tools") +
		gitRecordSep + rec("def456", "Sean", "2026-08-22", "fix: confine paths") +
		gitRecordSep + "'"

	got := formatGitLog(raw)
	// Order is hash, date, author, subject.
	if !strings.Contains(got, "abc123  2026-08-23  Sean  feat: add git tools") {
		t.Errorf("first commit mis-formatted:\n%s", got)
	}
	if !strings.Contains(got, "def456  2026-08-22  Sean  fix: confine paths") {
		t.Errorf("second commit mis-formatted:\n%s", got)
	}
	if strings.Contains(got, gitFieldSep) || strings.Contains(got, gitRecordSep) {
		t.Error("separators leaked into formatted output")
	}
}

func TestFormatGitLogEmpty(t *testing.T) {
	if got := formatGitLog(""); !strings.Contains(got, "No commits") {
		t.Errorf("empty log should say no commits, got %q", got)
	}
	if got := formatGitLog("''"); !strings.Contains(got, "No commits") {
		t.Errorf("quoted-empty log should say no commits, got %q", got)
	}
}

// A malformed record (too few fields) must be skipped, not panic or emit junk.
func TestFormatGitLogMalformed(t *testing.T) {
	raw := "'" + "onlyonefield" + gitRecordSep +
		strings.Join([]string{"h", "a", "d", "s"}, gitFieldSep) + "'"
	got := formatGitLog(raw)
	if !strings.Contains(got, "h  d  a  s") {
		t.Errorf("valid record after malformed one should still format: %s", got)
	}
}

// TestGitToolsNoShellInjection proves the git tools do not shell-interpret
// their arguments. It attempts an injection whose ONLY observable effect would
// be a side effect (creating a sentinel file); if the shell ran, the file
// appears. A string match on output is NOT used, because git echoes the ref
// back in its error message (which would false-positive). This guards the
// command-injection fix: git tools use argv exec (agent.ExecArgv), not a shell.
func TestGitToolsNoShellInjection(t *testing.T) {
	dir := t.TempDir()
	s := &agent.Sandbox{Root: dir}
	sentinel := filepath.Join(dir, "PWNED")

	// Each of these would create the sentinel IF the argument were shell-parsed.
	payloads := []func(){
		func() { toolGitShow(s, agent.ToolArgs{"ref": "HEAD; touch " + sentinel}) },
		func() { toolGitShow(s, agent.ToolArgs{"ref": "$(touch " + sentinel + ")"}) },
		func() { toolGitLog(s, agent.ToolArgs{"path": "x; touch " + sentinel}) },
		func() { toolGitBlame(s, agent.ToolArgs{"file": "y; touch " + sentinel}) },
	}
	for _, p := range payloads {
		p()
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("COMMAND INJECTION: a shell metacharacter payload created the sentinel file")
	}
}
