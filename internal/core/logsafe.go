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
func LogSafe(s string) string {
	return strings.Map(func(r rune) rune {
		// Drop C0 controls (0x00–0x1F) and DEL (0x7F). This includes CR (\r)
		// and LF (\n), the primary log-forging characters. Tab (0x09) is also
		// dropped to keep single-line log entries columnar-stable; callers that
		// want tabs in output aren't logging user input.
		if r < 0x20 || r == 0x7f {
			return -1 // -1 tells strings.Map to drop the rune
		}
		return r
	}, s)
}
