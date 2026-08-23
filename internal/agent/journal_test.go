package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davasorus/computah/internal/core"
)

func TestWriteJournal(t *testing.T) {
	v := setupVault(t)
	oldJ, oldSum := journalEnabled, summarizeSession
	journalEnabled = true
	summarizeSession = func(m []core.Message) (string, error) {
		return "- fixed the repository mapping\n- build green", nil
	}
	defer func() { journalEnabled, summarizeSession = oldJ, oldSum }()

	msgs := []core.Message{{Role: "user", Content: "fix the mapping"}}
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
