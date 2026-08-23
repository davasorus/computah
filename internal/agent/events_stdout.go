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
package agent

import "fmt"

type stdoutSubscriber struct {
	silent bool // TUI sets this so stdout doesn't fight the full-screen UI
}

var stdoutSub = &stdoutSubscriber{}

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
	case EvToolCall:
		suffix := ""
		if e.Meta["inline"] == "1" {
			suffix = " [inline]"
		}
		fmt.Println(tint(cDim, "  ⚙ "+e.Tool+"("+e.Text+")"+suffix))
	case EvToolDone:
		if e.Text != "" {
			fmt.Println(tint(cDim, "    "+e.Text))
		}
	case EvError:
		fmt.Println(tint(cRed, e.Text))
	case EvStatus:
		fmt.Println(e.Text)
	case EvThinking:
		// Thinking progress is handled by the live spinner today; the bus
		// event exists for the TUI/dashboard. stdout stays quiet to avoid
		// double-rendering against the spinner.
	case EvStats:
		fmt.Println(tint(cDim, e.Text))
	case EvLine:
		if e.Meta["raw"] == "1" {
			fmt.Println(e.Text) // pre-formatted (diff) — print verbatim
		} else if e.Color != "" {
			fmt.Println(tint(e.Color, e.Text))
		} else {
			fmt.Println(e.Text)
		}
	case EvUser:
		// The user already sees what they typed; no echo needed on stdout.
	}
}
