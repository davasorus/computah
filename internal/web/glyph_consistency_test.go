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
