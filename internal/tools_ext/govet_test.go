package toolsext

import (
	"strings"
	"testing"
)

func TestParseGoVet(t *testing.T) {
	root := "/home/user/proj"
	out := strings.Join([]string{
		"# github.com/user/proj/internal/agent",
		"/home/user/proj/internal/agent/tools.go:42:6: undefined: frobnicate",
		"/home/user/proj/internal/agent/loop.go:10: missing return",
		"go: downloading something", // noise, must be dropped
		"internal/web/headless.go:85:3: declared and not used: x",
	}, "\n")

	diags := parseGoVet(out, root)
	if len(diags) != 3 {
		t.Fatalf("expected 3 diagnostics, got %d: %v", len(diags), diags)
	}
	// Absolute paths under root are relativized.
	if !strings.HasPrefix(diags[0], "internal/agent/tools.go:42:6:") {
		t.Errorf("path not relativized / col dropped: %q", diags[0])
	}
	// line-only (no column) form is accepted.
	if !strings.HasPrefix(diags[1], "internal/agent/loop.go:10:") {
		t.Errorf("line-only diagnostic mis-parsed: %q", diags[1])
	}
	// already-relative paths pass through.
	if !strings.HasPrefix(diags[2], "internal/web/headless.go:85:3:") {
		t.Errorf("relative path mangled: %q", diags[2])
	}
	// the "# pkg" header and "go:" line must not appear as diagnostics.
	for _, d := range diags {
		if strings.HasPrefix(d, "#") || strings.HasPrefix(d, "go:") {
			t.Errorf("noise leaked into diagnostics: %q", d)
		}
	}
}

func TestParseGoVetEmpty(t *testing.T) {
	if d := parseGoVet("", "/x"); len(d) != 0 {
		t.Errorf("empty input should yield no diagnostics, got %v", d)
	}
	if d := parseGoVet("# just a header\ngo: noise", "/x"); len(d) != 0 {
		t.Errorf("noise-only input should yield no diagnostics, got %v", d)
	}
}
