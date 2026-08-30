// stdout subscriber — renders bus events to the terminal.
//
// This is the first (and during migration, primary) event subscriber. It
// reproduces the exact terminal styling the direct fmt.Println sites use, so
// when a print site is migrated from fmt.Println("x") to emitLine("x") the
// visible output is unchanged. The TUI and dashboard are alternate
// subscribers that will render the same events differently.
//
// The color hint on an event maps to the same ANSI codes tint() uses. A
// subscriber may be silenced (the TUI installs itself and silences stdout,
// since it owns the screen).
//
// In-place lines (EvOverwrite, e.g. a spinner or progress counter) are a
// special case: at most one is "open" at a time. Opening one erases and
// replaces whatever was there before; any OTHER event first erases it, the
// same way term.go's spinner erases itself the moment real output starts, so
// a fast-moving status line never leaves stale text mixed into the
// transcript.
package agent

import (
	"fmt"
	"sync"

	"github.com/davasorus/computah/internal/core"
)

type stdoutSubscriber struct {
	silent bool // TUI sets this so stdout doesn't fight the full-screen UI

	mu       sync.Mutex
	owActive bool // an EvOverwrite line is currently open on the terminal
}

var stdoutSub = &stdoutSubscriber{}

// eraseOverwrite erases the currently-open in-place line, if any. Callers
// hold s.mu.
func (s *stdoutSubscriber) eraseOverwriteLocked() {
	if s.owActive {
		fmt.Print("\r\033[K")
		s.owActive = false
	}
}

// println erases any open overwrite line, then prints a normal line.
func (s *stdoutSubscriber) println(line string) {
	s.mu.Lock()
	s.eraseOverwriteLocked()
	s.mu.Unlock()
	fmt.Println(line)
}

func (s *stdoutSubscriber) OnEvent(e Event) {
	if s.silent {
		return
	}
	switch e.Kind {
	case EvToken, EvAssistant:
		// Assistant content is printed directly to the terminal by the
		// mdWriter (with ANSI markdown rendering). These events exist ONLY
		// for alternate subscribers (dashboard/TUI); the stdout subscriber
		// must ignore them or the terminal double-prints.
		return
	case EvOverwrite:
		s.onOverwrite(e)
	case EvToolCall:
		suffix := ""
		if e.Meta["inline"] == "1" {
			suffix = " [inline]"
		}
		st := core.StyleFor(EvToolCall)
		s.println(tint(cDim, st.Pad()+st.Glyph+" "+e.Tool+"("+e.Text+")"+suffix))
	case EvToolDone:
		if e.Text != "" {
			st := core.StyleFor(EvToolDone)
			s.println(tint(cDim, st.Pad()+st.Glyph+" "+e.Text))
		}
	case EvError:
		s.println(tint(core.ANSIForRole(core.RoleError), e.Text))
	case EvStatus:
		s.println(e.Text)
	case EvThinking:
		// Thinking progress is handled by the live spinner today; the bus
		// event exists for the TUI/dashboard. stdout stays quiet to avoid
		// double-rendering against the spinner.
	case EvStats:
		s.println(tint(core.ANSIForRole(core.RoleDim), e.Text))
	case EvLine:
		if e.Meta["raw"] == "1" {
			s.println(e.Text) // pre-formatted (diff) — print verbatim
		} else if e.Color != "" {
			s.println(tint(e.Color, e.Text))
		} else {
			s.println(e.Text)
		}
	case EvUser:
		// The user already sees what they typed; no echo needed on stdout.
	}
}

// onOverwrite renders an in-place update or clear. Without a real terminal
// (useColor false — piped/redirected output) there's no cursor to erase, so
// it degrades to plain scrolling lines rather than silently dropping the
// status entirely.
func (s *stdoutSubscriber) onOverwrite(e Event) {
	if !useColor {
		if e.Meta["clear"] != "1" && e.Text != "" {
			fmt.Println(e.Text)
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eraseOverwriteLocked()
	if e.Meta["clear"] == "1" {
		return
	}
	line := e.Text
	if e.Color != "" {
		line = tint(e.Color, line)
	}
	fmt.Print("\r\033[K" + line)
	s.owActive = true
}
