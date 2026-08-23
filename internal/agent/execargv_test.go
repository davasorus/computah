package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExecArgvNoShell confirms ExecArgv does not interpret shell metacharacters:
// an argument containing "; touch X" is passed verbatim to the program, not run.
func TestExecArgvNoShell(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "PWNED")
	// echo with an injection-looking argument: a shell would run touch; argv
	// exec just prints the literal string.
	out, code, err := ExecArgv(dir, "echo", "hello; touch "+sentinel)
	if err != nil {
		t.Fatalf("echo failed: %v", err)
	}
	if code != 0 {
		t.Errorf("echo exit code %d", code)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatal("ExecArgv shell-interpreted its argument (sentinel created)")
	}
	// The literal argument should appear in echo's output.
	if len(out) == 0 {
		t.Error("expected echo output")
	}
}

// TestExecArgvExitCode confirms a non-zero exit is reported as a code, not an error.
func TestExecArgvExitCode(t *testing.T) {
	out, code, err := ExecArgv(t.TempDir(), "false")
	if err != nil {
		t.Errorf("non-zero exit should not be a Go error, got %v", err)
	}
	if code == 0 {
		t.Error("expected non-zero exit code from `false`")
	}
	_ = out
}
