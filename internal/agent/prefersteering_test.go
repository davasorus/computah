package agent

import (
	"strings"
	"testing"
)

func TestPreferSteering(t *testing.T) {
	saveP, saveH := preferredMCP, preferHints
	defer func() { preferredMCP, preferHints = saveP, saveH }()

	// No preferred servers → empty.
	preferredMCP, preferHints = nil, nil
	if got := preferSteering(); got != "" {
		t.Errorf("no servers should yield empty, got %q", got)
	}

	// Default (notes) server: uses the knowledge/notes wording.
	preferredMCP, preferHints = []string{"vault"}, nil
	got := preferSteering()
	if !strings.Contains(got, "knowledge/notes server") || !strings.Contains(got, "vault") {
		t.Errorf("default steering missing notes wording: %q", got)
	}

	// Custom-hint server (e.g. a sandbox): uses its own text, NOT the notes text.
	preferredMCP = []string{"sbx"}
	preferHints = map[string]string{"sbx": "a locked-down sandbox — run any untrusted or unfamiliar code here first."}
	got = preferSteering()
	if !strings.Contains(got, "locked-down sandbox") {
		t.Errorf("custom hint not used: %q", got)
	}
	if strings.Contains(got, "knowledge/notes server") {
		t.Errorf("custom-hint server must NOT get notes wording: %q", got)
	}
	if !strings.Contains(got, `"sbx"`) {
		t.Errorf("custom steering should name the server: %q", got)
	}

	// Mixed: a notes server AND a sandbox — both represented, correctly.
	preferredMCP = []string{"vault", "sbx"}
	got = preferSteering()
	if !strings.Contains(got, "knowledge/notes server") || !strings.Contains(got, "vault") {
		t.Errorf("mixed: notes part missing: %q", got)
	}
	if !strings.Contains(got, "locked-down sandbox") {
		t.Errorf("mixed: sandbox part missing: %q", got)
	}
}
