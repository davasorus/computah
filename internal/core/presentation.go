package core

// presentation.go is the single source of truth for how each event kind is
// presented — glyph, semantic role, and indent — so the CLI, TUI, and web
// dashboard render the same event the same way. Before this, each renderer
// hardcoded its own glyphs and styling and they drifted (a tool call was
// "⚙" in three places by luck, not by contract; colors and which kinds got
// special treatment diverged). Renderers now read EventStyle instead of
// inventing their own, so consistency is structural, not coincidental.

// Role is the semantic color/emphasis category for an event, independent of
// the concrete palette a given renderer uses (ANSI code, lipgloss style, or
// CSS class). Each renderer maps Role → its own styling once, centrally.
type Role string

const (
	RolePlain     Role = "plain"     // default text
	RoleDim       Role = "dim"       // de-emphasized (tool traces, stats)
	RoleError     Role = "error"     // errors
	RoleStatus    Role = "status"    // connection/mode status
	RoleAssistant Role = "assistant" // assistant content
	RoleUser      Role = "user"      // the user's own message
	RoleAccent    Role = "accent"    // highlighted (thinking, approvals)
)

// EventStyle is the canonical presentation for an event kind: the glyph that
// prefixes it, its semantic role, and how deeply it's indented (in spaces at
// the CLI; renderers may map indent to their own layout).
type EventStyle struct {
	Glyph  string // leading glyph, e.g. "⚙" — empty for content that shouldn't be decorated
	Role   Role
	Indent int // leading spaces before the glyph
}

// eventStyles is the canonical map. Keep glyphs in ONE place; every renderer
// reads them here. Kinds that renderers handle specially (streamed tokens,
// busy/thinking lifecycle) still have an entry so the mapping is complete and
// documented, even when a given surface chooses a live widget over a glyph.
var eventStyles = map[EventKind]EventStyle{
	EvLine:      {Glyph: "", Role: RolePlain, Indent: 0},
	EvToken:     {Glyph: "", Role: RoleAssistant, Indent: 0},
	EvThinking:  {Glyph: "…", Role: RoleAccent, Indent: 2},
	EvToolCall:  {Glyph: "⚙", Role: RoleDim, Indent: 2},
	EvToolDone:  {Glyph: "↳", Role: RoleDim, Indent: 4},
	EvUser:      {Glyph: "❯", Role: RoleUser, Indent: 0},
	EvAssistant: {Glyph: "", Role: RoleAssistant, Indent: 0},
	EvStats:     {Glyph: "∑", Role: RoleDim, Indent: 0},
	EvStatus:    {Glyph: "•", Role: RoleStatus, Indent: 0},
	EvError:     {Glyph: "✗", Role: RoleError, Indent: 0},
	EvBusy:      {Glyph: "", Role: RoleDim, Indent: 0},
	EvApproval:  {Glyph: "?", Role: RoleAccent, Indent: 2},
}

// Pad returns the leading indentation as spaces.
func (s EventStyle) Pad() string {
	if s.Indent <= 0 {
		return ""
	}
	return spaces(s.Indent)
}

func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

// StyleFor returns the canonical presentation for a kind. Unknown kinds get a
// plain default, so a newly added kind renders sanely everywhere until it's
// given an explicit entry.
func StyleFor(k EventKind) EventStyle {
	if s, ok := eventStyles[k]; ok {
		return s
	}
	return EventStyle{Glyph: "", Role: RolePlain, Indent: 0}
}
