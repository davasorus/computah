// Obsidian vault integration — the knowledge-base tools.
//
// An Obsidian vault is a folder of markdown; that's the whole trick. With
// "vault_path" set in ~/.agent/config.json, three tools join the registry:
//
//	vault_search  grep across every note (read-only, parallel-safe)
//	vault_read    a note by name — fuzzy: "SQL AG setup" finds
//	              "SQL AG Setup.md" anywhere in the vault; outgoing
//	              [[wikilinks]] are listed so the model can follow them
//	vault_note    WRITE a note — but only under <vault>/agent/. The agent
//	              gets a journal, not the keys: your personal notes are
//	              structurally out of reach, and everything it records
//	              shows up in your graph under one folder you can prune.
//
// The write direction is the quiet killer feature: "record that decision in
// the vault" gives the agent durable memory across sessions — architecture
// choices, gotchas, runbooks — in YOUR knowledge base instead of a transcript
// nobody rereads. Works whether Obsidian is running or not; it's just files.
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/core"
)

// core.VaultPath is the vault root (config "vault_path"); empty = tools absent.

// core.VaultAgentDir is the only place vault_note may write.

// ---------- Session auto-journal ----------
//
// With "journal": true in config (and a vault configured), every session
// that did real work ends with an aux-model summary appended to
// agent/journal.md — a dated diary of what the agent did, across every
// project, accumulating in your graph for free. Best-effort like titles:
// never delays exit, never errors loudly.

// summarizeSession is swappable for tests.
var summarizeSession = func(messages []Message) (string, error) {
	var parts []string
	for _, m := range messages {
		switch m.Role {
		case "user":
			if !strings.HasPrefix(m.Content, "[") {
				parts = append(parts, "USER: "+clip(m.Content, 300))
			}
		case "assistant":
			if m.Content != "" {
				parts = append(parts, "AGENT: "+clip(m.Content, 300))
			}
		}
		if len(parts) >= 24 {
			break
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("nothing to summarize")
	}
	req := []Message{
		{Role: "system", Content: "Summarize this coding session as 2-5 terse markdown bullet points: what was done, key decisions, open items. No heading, no preamble — bullets only."},
		{Role: "user", Content: strings.Join(parts, "\n")},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	reply, err := chat(ctx, curBaseURL, auxModelFor(), req, func(string) {})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(reply.Content), nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// writeJournal appends a dated entry to <vault>/agent/journal.md.
func writeJournal(workdir, title string, messages []Message) {
	if core.VaultPath == "" || !journalEnabled {
		return
	}
	summary, err := summarizeSession(messages)
	if err != nil || summary == "" {
		return
	}
	dir := filepath.Join(core.VaultPath, core.VaultAgentDir)
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	head := fmt.Sprintf("## %s — %s", time.Now().Format("2006-01-02 15:04"), filepath.Base(workdir))
	if title != "" {
		head += " — " + title
	}
	entry := "\n" + head + "\n\n" + summary + "\n"
	p := filepath.Join(dir, "journal.md")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.Size() == 0 {
		_, _ = f.WriteString("# Agent journal\n")
	}
	if _, err := f.WriteString(entry); err == nil {
		fmt.Println("journaled to vault: agent/journal.md")
	}
}

// journalEnabled gates the auto-journal (config "journal").
var journalEnabled bool
