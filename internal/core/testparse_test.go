package core

import (
	"strings"
	"testing"
)

func TestParseTestFailures(t *testing.T) {
	out := `=== RUN   TestFoo
--- FAIL: TestFoo (0.00s)
    foo_test.go:42: expected 3, got 4
=== RUN   TestBar
--- PASS: TestBar (0.00s)
--- FAIL: TestBaz (0.01s)
    baz_test.go:10: nil pointer
FAIL
FAIL    example  0.02s`
	got := ParseTestFailures(out)
	if !strings.Contains(got, "TestFoo") || !strings.Contains(got, "TestBaz") {
		t.Fatalf("must list failing tests: %s", got)
	}
	if !strings.Contains(got, "foo_test.go:42") || !strings.Contains(got, "expected 3, got 4") {
		t.Fatalf("must extract assertions with file:line: %s", got)
	}
	if strings.Contains(got, "TestBar") {
		t.Fatalf("passing tests must not appear: %s", got)
	}
}

func TestParseTestFailuresBuildError(t *testing.T) {
	out := "# github.com/x/y\n./db.go:16:11: undefined: String\nFAIL"
	got := ParseTestFailures(out)
	if !strings.Contains(got, "Build errors") || !strings.Contains(got, "undefined: String") {
		t.Fatalf("build errors must surface: %s", got)
	}
}

func TestParseTestFailuresClean(t *testing.T) {
	if got := ParseTestFailures("ok  example  0.5s\n"); got != "" {
		t.Fatalf("clean output must parse to empty, got %q", got)
	}
}
