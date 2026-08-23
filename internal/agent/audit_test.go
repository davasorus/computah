package agent

import (
	"github.com/davasorus/computah/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordActivity(t *testing.T) {
	activity.Lock()
	activity.toolCounts = map[string]int{}
	activity.vaultNotes, activity.commands, activity.errors = nil, nil, 0
	activity.Unlock()

	recordActivity("read_file", map[string]any{"path": "x.go"}, "contents")
	recordActivity("read_file", map[string]any{"path": "y.go"}, "contents")
	recordActivity("vault_note", map[string]any{"title": "Decision"}, "OK")
	recordActivity("run_command", map[string]any{"command": "go build ./..."}, "OK")
	recordActivity("edit_file", map[string]any{"path": "z.go"}, "ERROR: not found")

	activity.Lock()
	defer activity.Unlock()
	if activity.toolCounts["read_file"] != 2 {
		t.Fatalf("read_file count: %d", activity.toolCounts["read_file"])
	}
	if activity.errors != 1 {
		t.Fatalf("errors: %d", activity.errors)
	}
	if len(activity.vaultNotes) != 1 || activity.vaultNotes[0] != "Decision" {
		t.Fatalf("vault notes: %v", activity.vaultNotes)
	}
	if len(activity.commands) != 1 || !strings.Contains(activity.commands[0], "go build") {
		t.Fatalf("commands: %v", activity.commands)
	}
}

func TestWriteAuditNote(t *testing.T) {
	v := setupVault(t)
	oldAudit := auditEnabled
	auditEnabled = true
	defer func() { auditEnabled = oldAudit }()

	activity.Lock()
	activity.toolCounts = map[string]int{"read_file": 3, "vault_note": 1, "edit_files": 2}
	activity.vaultNotes = []string{"Decision Log"}
	activity.commands = []string{"go test ./..."}
	activity.errors = 1
	activity.Unlock()

	sb := &Sandbox{Root: t.TempDir(), Modified: []string{filepath.Join(t.TempDir(), "repo", "a.go")}}
	writeAuditNote("/mnt/z/proj9", "landed the DI refactor", sb)

	auditDir := filepath.Join(v, "agent", "audit")
	entries, err := os.ReadDir(auditDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one audit note, got %v (err %v)", entries, err)
	}
	data, _ := os.ReadFile(filepath.Join(auditDir, entries[0].Name()))
	s := string(data)
	// Frontmatter — the Dataview-queryable part.
	for _, want := range []string{"type: agent-audit", "project: proj9", "tool_calls: 6", "errors: 1", "tags: [agent-audit]"} {
		if !strings.Contains(s, want) {
			t.Fatalf("audit frontmatter missing %q:\n%s", want, s)
		}
	}
	// Body sections.
	if !strings.Contains(s, "read_file ×3") || !strings.Contains(s, "landed the DI refactor") {
		t.Fatalf("audit body incomplete:\n%s", s)
	}
	if !strings.Contains(s, "Decision Log") || !strings.Contains(s, "go test") {
		t.Fatalf("audit missing vault/command sections:\n%s", s)
	}
}

func TestAuditDisabledNoWrite(t *testing.T) {
	v := setupVault(t)
	oldAudit := auditEnabled
	auditEnabled = false
	defer func() { auditEnabled = oldAudit }()
	activity.Lock()
	activity.toolCounts = map[string]int{"read_file": 1}
	activity.Unlock()
	writeAuditNote("/mnt/z/proj9", "x", &Sandbox{Root: t.TempDir()})
	if _, err := os.Stat(filepath.Join(v, "agent", "audit")); !os.IsNotExist(err) {
		t.Fatal("disabled audit must not create the audit dir")
	}
}

// setupVault creates a temp vault dir and points core.VaultPath at it.
func setupVault(t *testing.T) string {
	t.Helper()
	v := t.TempDir()
	old := core.VaultPath
	core.VaultPath = v
	t.Cleanup(func() { core.VaultPath = old })
	return v
}
