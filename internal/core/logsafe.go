package core

import "strings"

// LogSafe neutralizes a user-controlled value before it is written to a log or
// terminal line. It strips the characters that let a caller forge or corrupt
// log output — CR and LF (which fabricate new log lines) and other C0 control
// characters and DEL (which can move the cursor, inject ANSI escapes, or hide
// text). Ordinary printable text, including Unicode, is passed through
// unchanged.
//
// Use this on any value that originates from user input (a typed path, a
// slash-command argument, a query) at the point it is interpolated into a
// log/print call. It addresses CWE-117 (log injection); it is NOT a shell or
// path sanitizer — see VetCommand and ConfinePath for those.
//
// Implementation note: CodeQL's go/log-injection query has a built-in
// sanitizer pattern (ReplaceSanitizer) that recognizes an expression
// equivalent to strings.ReplaceAll(s, "\r", ...) or strings.ReplaceAll(s,
// "\n", ...) as neutralizing a value. It does NOT recognize strings.Map, and
// Go is not yet in the set of languages CodeQL model packs support (so the
// custom barrier model in .github/codeql/extensions/ is inert for this query
// too). Routing CR/LF removal through strings.ReplaceAll — after strings.Map
// strips the other C0 controls and DEL — lets CodeQL confirm every call site
// is sanitized without relying on either mechanism.
func LogSafe(s string) string {
	stripped := strings.Map(func(r rune) rune {
		// Leave CR and LF alone here; they are removed below via
		// strings.ReplaceAll so CodeQL's sanitizer pattern applies. Drop
		// every other C0 control (0x00–0x1F) and DEL (0x7F). Tab (0x09) is
		// also dropped to keep single-line log entries columnar-stable;
		// callers that want tabs in output aren't logging user input.
		if r == '\r' || r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1 // -1 tells strings.Map to drop the rune
		}
		return r
	}, s)
	stripped = strings.ReplaceAll(stripped, "\r", "")
	return strings.ReplaceAll(stripped, "\n", "")
}
