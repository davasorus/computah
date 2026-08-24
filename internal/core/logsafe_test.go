package core

import "testing"

func TestLogSafe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"normal/path/file.go", "normal/path/file.go"},
		{"with\nnewline", "withnewline"},
		{"with\r\ncrlf", "withcrlf"},
		{"forged\n2024 ERROR fake log line", "forged2024 ERROR fake log line"},
		{"tab\there", "tabhere"},
		{"bell\x07and\x1bescape", "bellandescape"},
		{"del\x7fchar", "delchar"},
		{"unicode: café résumé 日本語", "unicode: café résumé 日本語"},
		{"", ""},
	}
	for _, c := range cases {
		if got := LogSafe(c.in); got != c.want {
			t.Errorf("LogSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLogSafeStopsForgery is the security property: no CR/LF survives, so a
// second log line cannot be fabricated from user input.
func TestLogSafeStopsForgery(t *testing.T) {
	payload := "ok\n[ERROR] injected admin login from 10.0.0.1"
	out := LogSafe(payload)
	for _, r := range out {
		if r == '\n' || r == '\r' {
			t.Fatalf("LogSafe left a line break in %q", out)
		}
	}
}
