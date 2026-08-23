package agent

import "strings"

// InputKind classifies a submitted line so any front-end (REPL, TUI, headless
// web) routes it the same way.
type InputKind int

const (
	InputPrompt  InputKind = iota // normal message to the model
	InputShell                    // !cmd — run directly
	InputFile                     // @path — attach a file
	InputCommand                  // /cmd — slash command
	InputBlank                    // empty
)

// ClassifyInput determines how a submitted line should be handled.
func ClassifyInput(line string) (InputKind, string) {
	t := strings.TrimSpace(line)
	switch {
	case t == "":
		return InputBlank, ""
	case strings.HasPrefix(t, "!"):
		return InputShell, strings.TrimSpace(t[1:])
	case strings.HasPrefix(t, "@"):
		return InputFile, strings.TrimSpace(t[1:])
	case strings.HasPrefix(t, "/"):
		return InputCommand, t
	default:
		return InputPrompt, line
	}
}
