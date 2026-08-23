// Rich input for the TUI — history, tab-completion, multi-line, and the
// !shell / @file / /command affordances the REPL has.
//
// The REPL gets these from x/term's line editor; bubbletea's textinput is
// bare, so we layer them here. Completion reuses the REPL's completeLine and
// replCommands (single source of truth for the command list). History reads
// the same ~/.agent/history file the REPL persists, so recall is shared
// across both front-ends.
//
// What's handled here (logic, unit-tested):
//   - history ring: up/down recall, shared file
//   - tab completion: delegates to completeLine
//   - multi-line: a """ line toggles a multi-line buffer
//   - prefix routing: !shell, @file, /command classified for the caller
//
// The keybindings are wired in tui.go's Update; this file is the testable
// core so the behavior is verified even though the live feel isn't.
package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// tuiHistory is an in-memory recall ring seeded from the shared history file.
type tuiHistory struct {
	entries []string
	idx     int    // cursor for up/down; len(entries) == "not recalling"
	draft   string // the in-progress line stashed when recall starts
}

func newTUIHistory() *tuiHistory {
	h := &tuiHistory{}
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(home, ".agent", "history")); err == nil {
			for _, l := range strings.Split(string(data), "\n") {
				if l != "" {
					h.entries = append(h.entries, l)
				}
			}
		}
	}
	h.idx = len(h.entries)
	return h
}

// add records a submitted line (skips blanks and immediate duplicates).
func (h *tuiHistory) add(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if n := len(h.entries); n > 0 && h.entries[n-1] == line {
		h.idx = len(h.entries)
		return
	}
	h.entries = append(h.entries, line)
	h.idx = len(h.entries)
}

// up recalls the previous entry. current is the live input (stashed as draft
// the first time recall begins). Returns the value to show.
func (h *tuiHistory) up(current string) string {
	if len(h.entries) == 0 {
		return current
	}
	if h.idx == len(h.entries) {
		h.draft = current // stash the in-progress line
	}
	if h.idx > 0 {
		h.idx--
	}
	return h.entries[h.idx]
}

// down moves toward the present; past the newest entry restores the draft.
func (h *tuiHistory) down() string {
	if h.idx >= len(h.entries) {
		return h.draft
	}
	h.idx++
	if h.idx == len(h.entries) {
		return h.draft
	}
	return h.entries[h.idx]
}

// resetRecall ends recall mode (called on any edit that isn't up/down).
func (h *tuiHistory) resetRecall() { h.idx = len(h.entries) }

// tuiComplete applies tab completion to (line, pos), reusing the REPL's
// completeLine. Returns the new line, new cursor pos, and whether it changed.
func tuiComplete(line string, pos int) (string, int, bool) {
	return completeLine(line, pos, '\t')
}

// inputKind classifies a submitted line so the TUI's Update can route it the
// same way the REPL does.
type inputKind int

const (
	inputPrompt  inputKind = iota // normal message to the model
	inputShell                    // !cmd — run directly
	inputFile                     // @path — attach a file
	inputCommand                  // /cmd — slash command
	inputBlank                    // empty
)

// classifyInput determines how a submitted line should be handled.
func classifyInput(line string) (inputKind, string) {
	t := strings.TrimSpace(line)
	switch {
	case t == "":
		return inputBlank, ""
	case strings.HasPrefix(t, "!"):
		return inputShell, strings.TrimSpace(t[1:])
	case strings.HasPrefix(t, "@"):
		return inputFile, strings.TrimSpace(t[1:])
	case strings.HasPrefix(t, "/"):
		return inputCommand, t
	default:
		return inputPrompt, line
	}
}

// isMultilineToggle reports whether a line is the """ multi-line delimiter.
func isMultilineToggle(line string) bool {
	return strings.TrimSpace(line) == `"""`
}
