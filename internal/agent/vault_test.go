package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupVault(t *testing.T) string {
	t.Helper()
	v := t.TempDir()
	os.MkdirAll(filepath.Join(v, "Infrastructure"), 0o755)
	os.MkdirAll(filepath.Join(v, ".obsidian"), 0o755)
	os.WriteFile(filepath.Join(v, "Infrastructure", "SQL AG Setup.md"),
		[]byte("# SQL AG Setup\n\nUse RegisterAllProvidersIP=0 for multi-subnet.\nSee [[GovCloud Networking]] and [[SSRS Auth Fix]].\n"), 0o644)
	os.WriteFile(filepath.Join(v, "GovCloud Networking.md"),
		[]byte("# GovCloud Networking\n\nENI secondary IPs must be assigned manually.\n"), 0o644)
	os.WriteFile(filepath.Join(v, ".obsidian", "workspace.md"),
		[]byte("should never be found\n"), 0o644)
	old := vaultPath
	vaultPath = v
	t.Cleanup(func() { vaultPath = old })
	return v
}

func TestVaultSearch(t *testing.T) {
	setupVault(t)
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.toolVaultSearch(toolArgs{"query": "registerallprovidersip"})
	if !strings.Contains(out, "Infrastructure/SQL AG Setup") || !strings.Contains(out, "multi-subnet") {
		t.Fatalf("case-insensitive search failed:\n%s", out)
	}
	if out := sb.toolVaultSearch(toolArgs{"query": "never be found"}); !strings.Contains(out, "no notes match") {
		t.Fatalf(".obsidian must be excluded:\n%s", out)
	}
}

func TestVaultReadFuzzyAndLinks(t *testing.T) {
	setupVault(t)
	sb := &Sandbox{Root: t.TempDir()}
	out := sb.toolVaultRead(toolArgs{"note": "sql ag"})
	if !strings.Contains(out, "RegisterAllProvidersIP") {
		t.Fatalf("fuzzy partial-name read failed:\n%s", out)
	}
	if !strings.Contains(out, "Outgoing links: [[GovCloud Networking]], [[SSRS Auth Fix]]") {
		t.Fatalf("wikilinks missing:\n%s", out)
	}
	if out := sb.toolVaultRead(toolArgs{"note": "nonexistent zettel"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatalf("missing note must error: %s", out)
	}
}

func TestVaultNoteSandbox(t *testing.T) {
	v := setupVault(t)
	sb := &Sandbox{Root: t.TempDir()}
	// Create, then append.
	out := sb.toolVaultNote(toolArgs{"title": "proj9 decisions", "content": "Chose lib/pq over pgx for now.", "mode": "create"})
	if !strings.HasPrefix(out, "OK") {
		t.Fatalf("create failed: %s", out)
	}
	out = sb.toolVaultNote(toolArgs{"title": "proj9 decisions", "content": "Liquibase owns the schema."})
	if !strings.HasPrefix(out, "OK") {
		t.Fatalf("append failed: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(v, "agent", "proj9 decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "lib/pq") || !strings.Contains(s, "Liquibase") || !strings.Contains(s, "## 20") {
		t.Fatalf("note content wrong:\n%s", s)
	}
	// Escape attempts are rejected, not cleaned.
	for _, evil := range []string{"../personal-journal", "Infrastructure/overwrite-me", "a\\b"} {
		if out := sb.toolVaultNote(toolArgs{"title": evil, "content": "x"}); !strings.HasPrefix(out, "ERROR") {
			t.Fatalf("title %q must be rejected: %s", evil, out)
		}
	}
	// create refuses to clobber.
	if out := sb.toolVaultNote(toolArgs{"title": "proj9 decisions", "content": "x", "mode": "create"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatalf("create must not clobber: %s", out)
	}
}

func TestWriteJournal(t *testing.T) {
	v := setupVault(t)
	oldJ, oldSum := journalEnabled, summarizeSession
	journalEnabled = true
	summarizeSession = func(m []Message) (string, error) {
		return "- fixed the repository mapping\n- build green", nil
	}
	defer func() { journalEnabled, summarizeSession = oldJ, oldSum }()

	msgs := []Message{{Role: "user", Content: "fix the mapping"}}
	writeJournal("/mnt/z/proj9", "mapping fix", msgs)
	writeJournal("/mnt/z/proj9", "second entry", msgs)
	data, err := os.ReadFile(filepath.Join(v, "agent", "journal.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "# Agent journal") {
		t.Fatalf("header once at top:\n%s", s)
	}
	if strings.Count(s, "## 20") != 2 || !strings.Contains(s, "proj9 — mapping fix") || !strings.Contains(s, "build green") {
		t.Fatalf("two dated entries expected:\n%s", s)
	}
	// Disabled → no write.
	journalEnabled = false
	writeJournal("/mnt/z/other", "", msgs)
	data2, _ := os.ReadFile(filepath.Join(v, "agent", "journal.md"))
	if len(data2) != len(data) {
		t.Fatal("disabled journal must not write")
	}
}
