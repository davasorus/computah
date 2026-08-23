package toolsext

import (
	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
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
	old := core.VaultPath
	core.VaultPath = v
	t.Cleanup(func() { core.VaultPath = old })
	return v
}

func TestVaultSearch(t *testing.T) {
	setupVault(t)
	sb := &agent.Sandbox{Root: t.TempDir()}
	out := toolVaultSearch(sb, agent.ToolArgs{"query": "registerallprovidersip"})
	if !strings.Contains(out, "Infrastructure/SQL AG Setup") || !strings.Contains(out, "multi-subnet") {
		t.Fatalf("case-insensitive search failed:\n%s", out)
	}
	if out := toolVaultSearch(sb, agent.ToolArgs{"query": "never be found"}); !strings.Contains(out, "no notes match") {
		t.Fatalf(".obsidian must be excluded:\n%s", out)
	}
}

func TestVaultReadFuzzyAndLinks(t *testing.T) {
	setupVault(t)
	sb := &agent.Sandbox{Root: t.TempDir()}
	out := toolVaultRead(sb, agent.ToolArgs{"note": "sql ag"})
	if !strings.Contains(out, "RegisterAllProvidersIP") {
		t.Fatalf("fuzzy partial-name read failed:\n%s", out)
	}
	if !strings.Contains(out, "Outgoing links: [[GovCloud Networking]], [[SSRS Auth Fix]]") {
		t.Fatalf("wikilinks missing:\n%s", out)
	}
	if out := toolVaultRead(sb, agent.ToolArgs{"note": "nonexistent zettel"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatalf("missing note must error: %s", out)
	}
}

func TestVaultNoteSandbox(t *testing.T) {
	v := setupVault(t)
	sb := &agent.Sandbox{Root: t.TempDir()}
	// Create, then append.
	out := toolVaultNote(sb, agent.ToolArgs{"title": "proj9 decisions", "content": "Chose lib/pq over pgx for now.", "mode": "create"})
	if !strings.HasPrefix(out, "OK") {
		t.Fatalf("create failed: %s", out)
	}
	out = toolVaultNote(sb, agent.ToolArgs{"title": "proj9 decisions", "content": "Liquibase owns the schema."})
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
		if out := toolVaultNote(sb, agent.ToolArgs{"title": evil, "content": "x"}); !strings.HasPrefix(out, "ERROR") {
			t.Fatalf("title %q must be rejected: %s", evil, out)
		}
	}
	// create refuses to clobber.
	if out := toolVaultNote(sb, agent.ToolArgs{"title": "proj9 decisions", "content": "x", "mode": "create"}); !strings.HasPrefix(out, "ERROR") {
		t.Fatalf("create must not clobber: %s", out)
	}
}
