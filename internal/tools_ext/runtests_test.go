package toolsext

import "testing"

func TestTestPattern(t *testing.T) {
	cases := map[string]string{
		"":                 "./...",
		"   ":              "./...",
		"internal/agent":   "./internal/agent",
		"./internal/core":  "./internal/core",
		"/abs/path":        "/abs/path",
		"  internal/web  ": "./internal/web",
	}
	for in, want := range cases {
		if got := testPattern(in); got != want {
			t.Errorf("testPattern(%q) = %q, want %q", in, got, want)
		}
	}
}
