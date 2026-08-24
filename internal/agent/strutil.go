package agent

import "unicode/utf8"

// clip truncates s to at most n bytes, appending an ellipsis when it does.
// It respects UTF-8 boundaries so multibyte runes are never split.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Back up to a rune boundary at or before n.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
