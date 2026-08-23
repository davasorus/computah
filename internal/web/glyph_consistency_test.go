package web

import (
	"strings"
	"testing"

	"github.com/davasorus/computah/internal/core"
)

// TestDashboardGlyphsMatchCore guards against the dashboard's embedded JS
// glyph map drifting from the canonical core presentation spec. The dashboard
// HTML embeds glyphs as a static JS literal (it can't call Go at runtime), so
// this test is the contract that keeps it in sync: if core changes a glyph,
// this fails until the dashboard HTML is updated to match.
func TestDashboardGlyphsMatchCore(t *testing.T) {
	checks := map[core.EventKind]string{
		core.EvToolCall: "tool_call: '" + core.StyleFor(core.EvToolCall).Glyph + "'",
		core.EvToolDone: "tool_done: '" + core.StyleFor(core.EvToolDone).Glyph + "'",
		core.EvError:    "error: '" + core.StyleFor(core.EvError).Glyph + "'",
		core.EvStatus:   "status: '" + core.StyleFor(core.EvStatus).Glyph + "'",
		core.EvStats:    "stats: '" + core.StyleFor(core.EvStats).Glyph + "'",
		core.EvUser:     "user: '" + core.StyleFor(core.EvUser).Glyph + "'",
	}
	for kind, want := range checks {
		if !strings.Contains(dashHTML, want) {
			t.Errorf("dashboard HTML is missing canonical glyph for %q — expected JS %q; update the glyphs map in dashboard_html.go to match core.StyleFor", kind, want)
		}
	}
}

// TestDashboardColorsMatchRoles guards the dashboard's per-event CSS colors
// against drifting from the canonical roles. Tool traces and stats share the
// "dim" role across all surfaces; if someone recolors them in the dashboard
// without a matching role change, this fails. (Concrete hex differs per
// surface — we assert the role INTENT via the CSS var, not exact color.)
func TestDashboardColorsMatchRoles(t *testing.T) {
	// Events whose canonical role is RoleDim must use the --dim var.
	dimEvents := []string{".ev.tool_call .b", ".ev.tool_done .b", ".ev.stats .b"}
	for _, sel := range dimEvents {
		want := sel + " { color: var(--dim); }"
		if !strings.Contains(dashHTML, want) {
			t.Errorf("dashboard: %s should use var(--dim) to match its RoleDim across surfaces; expected CSS %q", sel, want)
		}
	}
	// Error stays red, user stays green — pin those too.
	if !strings.Contains(dashHTML, ".ev.error .b { color: var(--red); }") {
		t.Error("dashboard: error should be var(--red) (RoleError)")
	}
	if !strings.Contains(dashHTML, ".ev.user .b { color: var(--green); }") {
		t.Error("dashboard: user should be var(--green) (RoleUser)")
	}
}
