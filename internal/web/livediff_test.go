package web

import (
	"strings"
	"testing"
)

// TestDashboardHasDiffRendering asserts the live-diff rendering pieces are all
// present in the dashboard HTML: the CSS classes for colored diff lines and
// the JS functions that detect and render a diff block. A browser can't be
// driven here, so this guards that the wiring isn't accidentally removed.
func TestDashboardHasDiffRendering(t *testing.T) {
	needles := []string{
		".diff .dl.add",            // added-line CSS
		".diff .dl.del",            // removed-line CSS
		".diff .dl.hunk",           // hunk-header CSS
		"function looksLikeDiff",   // detector
		"function renderDiffBlock", // renderer
		"ev.meta.raw === '1'",      // the raw-diff branch in the event handler
	}
	for _, n := range needles {
		if !strings.Contains(dashHTML, n) {
			t.Errorf("dashboard HTML missing diff-rendering piece: %q", n)
		}
	}
}

// TestDashboardDiffUsesTextContent guards the XSS-safety choice: diff lines are
// injected via textContent (not innerHTML), so diff content can't inject HTML.
func TestDashboardDiffUsesTextContent(t *testing.T) {
	// The renderDiffBlock function must set textContent, not innerHTML.
	idx := strings.Index(dashHTML, "function renderDiffBlock")
	if idx < 0 {
		t.Fatal("renderDiffBlock not found")
	}
	fnBody := dashHTML[idx : idx+700]
	if !strings.Contains(fnBody, "textContent") {
		t.Error("renderDiffBlock should use textContent for safe escaping")
	}
	if strings.Contains(fnBody, "innerHTML") {
		t.Error("renderDiffBlock must not use innerHTML (XSS risk with diff content)")
	}
}
