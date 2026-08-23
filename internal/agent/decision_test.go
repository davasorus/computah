package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordDecisionWritesFrontmatter(t *testing.T) {
	v := setupVault(t)
	registerDecisionTool()
	defer delete(toolByName, "record_decision")

	sb := &Sandbox{Root: t.TempDir()}
	out := sb.toolRecordDecision(toolArgs{
		"title":   "Why PromoteIS needs GUID convergence",
		"type":    "rca",
		"project": "ims-promote",
		"tags":    "sql, guid, promoteis",
		"body":    "Conclusion: sqlutility mints independent GUIDs.\n\nReasoning: ...",
	})
	if strings.HasPrefix(out, "ERROR") {
		t.Fatalf("unexpected error: %s", out)
	}
	// Find the written file.
	dir := filepath.Join(v, "agent", "decisions")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 decision note, got %d", len(entries))
	}
	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	s := string(data)
	for _, want := range []string{"type: rca", "project: ims-promote", "tags: [rca, ims-promote, sql, guid, promoteis]", "# Why PromoteIS needs GUID convergence", "Conclusion:"} {
		if !strings.Contains(s, want) {
			t.Fatalf("note missing %q:\n%s", want, s)
		}
	}
	// Filename is dated + slugged.
	if !strings.Contains(entries[0].Name(), "why-promoteis-needs-guid-convergence") {
		t.Fatalf("filename not slugged: %s", entries[0].Name())
	}
}

func TestRecordDecisionDefaultsAndValidation(t *testing.T) {
	setupVault(t)
	sb := &Sandbox{Root: t.TempDir()}
	// missing body → error
	if out := sb.toolRecordDecision(toolArgs{"title": "x"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatal("missing body should error")
	}
	// bad type → error
	if out := sb.toolRecordDecision(toolArgs{"title": "x", "body": "y", "type": "bogus"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatal("bad type should error")
	}
	// default type = decision
	out := sb.toolRecordDecision(toolArgs{"title": "defaulted", "body": "b"})
	if strings.HasPrefix(out, "ERROR") {
		t.Fatalf("valid minimal call errored: %s", out)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Why PromoteIS needs GUID convergence": "why-promoteis-needs-guid-convergence",
		"  Trim & punctuation!!! ":             "trim-punctuation",
		"":                                     "note",
		"////":                                 "note",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Fatalf("slug(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRecordDecisionSurvivesMCPDedup(t *testing.T) {
	// record_decision must NOT be removed by the file-layer vault dedup
	// (it's not one of the three suppressed names).
	savedReg, savedIdx := registry, toolByName
	registry = []Tool{
		{Name: "vault_search"}, {Name: "vault_read"}, {Name: "vault_note"},
		{Name: "record_decision"},
	}
	toolByName = map[string]Tool{}
	for _, tl := range registry {
		toolByName[tl.Name] = tl
	}
	defer func() { registry, toolByName = savedReg, savedIdx }()

	unregisterTools("vault_search", "vault_read", "vault_note")
	if _, ok := toolByName["record_decision"]; !ok {
		t.Fatal("record_decision must survive the vault dedup")
	}
}
