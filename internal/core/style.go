package core

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// UseColor reports whether ANSI styling should be emitted: only when stdout is
// a real terminal and NO_COLOR is unset. Computed once at startup.
var UseColor = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

// Tint wraps s in the given ANSI SGR code (e.g. ColorRed), or returns s
// unchanged when color is disabled.
func Tint(code, s string) string {
	if !UseColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// ANSI SGR codes used across the UI.
const (
	ColorDim    = "2"
	ColorRed    = "31"
	ColorGreen  = "32"
	ColorYellow = "33"
	ColorCyan   = "36"
)

// TermWidth returns the terminal's column width, trying stdout/stderr/stdin,
// then $COLUMNS, then a conservative default of 80.
func TermWidth() int {
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd()), int(os.Stdin.Fd())} {
		if w, _, err := term.GetSize(fd); err == nil && w > 0 {
			return w
		}
	}
	if c := os.Getenv("COLUMNS"); c != "" {
		if w, err := strconv.Atoi(strings.TrimSpace(c)); err == nil && w > 0 {
			return w
		}
	}
	return 80 // conservative default — better to under-fill than overflow
}
