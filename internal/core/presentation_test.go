package core

import "testing"

// TestEveryEventKindHasAStyle ensures the canonical presentation map covers
// every declared EventKind. If someone adds a new kind without a style, this
// fails — forcing the glyph/role decision to be made ONCE, centrally, instead
// of drifting across the three renderers.
func TestEveryEventKindHasAStyle(t *testing.T) {
	allKinds := []EventKind{
		EvLine, EvToken, EvThinking, EvToolCall, EvToolDone,
		EvUser, EvAssistant, EvStats, EvStatus, EvError, EvBusy, EvApproval,
		EvOverwrite,
	}
	for _, k := range allKinds {
		if _, ok := eventStyles[k]; !ok {
			t.Errorf("EventKind %q has no entry in eventStyles — add one so all renderers agree", k)
		}
	}
}

func TestStyleForUnknownKindIsSafe(t *testing.T) {
	s := StyleFor(EventKind("some_future_kind"))
	if s.Role != RolePlain || s.Glyph != "" {
		t.Errorf("unknown kind should get a plain default, got %+v", s)
	}
}

func TestKnownGlyphsStable(t *testing.T) {
	// Pin the glyphs the renderers historically used, so a change is deliberate.
	cases := map[EventKind]string{
		EvToolCall: "⚙",
		EvToolDone: "↳",
		EvError:    "✗",
		EvUser:     "❯",
		EvStatus:   "•",
		EvStats:    "∑",
	}
	for k, want := range cases {
		if got := StyleFor(k).Glyph; got != want {
			t.Errorf("glyph for %q: got %q want %q", k, got, want)
		}
	}
}

func TestANSIForRole(t *testing.T) {
	cases := map[Role]string{
		RoleDim:    ColorDim,
		RoleError:  ColorRed,
		RoleUser:   ColorGreen,
		RoleAccent: ColorCyan,
		RolePlain:  "",
		RoleStatus: "",
	}
	for role, want := range cases {
		if got := ANSIForRole(role); got != want {
			t.Errorf("ANSIForRole(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestToolTracesAreDim(t *testing.T) {
	if StyleFor(EvToolCall).Role != RoleDim {
		t.Error("tool_call should be RoleDim for consistent de-emphasis across surfaces")
	}
	if StyleFor(EvToolDone).Role != RoleDim {
		t.Error("tool_done should be RoleDim for consistent de-emphasis across surfaces")
	}
	if StyleFor(EvError).Role != RoleError {
		t.Error("error should be RoleError across surfaces")
	}
}
