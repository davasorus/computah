package core

import "os"

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
