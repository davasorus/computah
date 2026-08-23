package toolsext

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
)

var wikilinkRe = regexp.MustCompile(`\[\[([^\]|#]+)`)

func RegisterVaultTools() {
	if core.VaultPath == "" {
		return
	}
	if fi, err := os.Stat(core.VaultPath); err != nil || !fi.IsDir() {
		fmt.Printf("vault: %s not accessible (%v) — vault tools disabled\n", core.VaultPath, err)
		core.VaultPath = ""
		return
	}
	agent.RegisterTools(
		agent.Tool{
			Name: "vault_search",
			Desc: "Search the user's Obsidian knowledge vault (personal notes: runbooks, decisions, infrastructure documentation). Case-insensitive substring match across all notes; returns note names with matching lines. Use this when the user references their notes, past decisions, or documented procedures.",
			Props: map[string]any{
				"query": map[string]any{"type": "string", "description": "Text to search for (case-insensitive)"},
			},
			Required: []string{"query"},
			Handler:  toolVaultSearch,
		},
		agent.Tool{
			Name: "vault_read",
			Desc: "Read a note from the user's Obsidian vault by name. Fuzzy: partial names and missing .md are fine. The result ends with the note's outgoing [[wikilinks]] — read those next when the topic continues elsewhere.",
			Props: map[string]any{
				"note": map[string]any{"type": "string", "description": "Note name or partial name, e.g. 'SQL AG setup'"},
			},
			Required: []string{"note"},
			Handler:  toolVaultRead,
		},
		agent.Tool{
			Name: "vault_note",
			Desc: "Write to the agent's journal inside the user's Obsidian vault (the vault's agent/ folder ONLY). Use when the user asks to record a decision, gotcha, or runbook — or when finishing significant work whose reasoning deserves to outlive this session. mode 'append' adds a timestamped section to an existing note; 'create' starts a new one.",
			Props: map[string]any{
				"title":   map[string]any{"type": "string", "description": "Note title, e.g. 'proj9 decisions' (becomes agent/proj9 decisions.md)"},
				"content": map[string]any{"type": "string", "description": "Markdown content to write"},
				"mode":    map[string]any{"type": "string", "enum": []string{"append", "create"}, "description": "append (default) adds a timestamped section; create starts fresh (fails if the note exists)"},
			},
			Required: []string{"title", "content"},
			Handler:  toolVaultNote,
		},
	)
	agent.MarkReadOnly("vault_search")
	agent.MarkReadOnly("vault_read")
	agent.RebuildToolSchemas()
	fmt.Printf("vault: %s connected (vault_search / vault_read / vault_note→agent/)\n", core.VaultPath)
}

// vaultNotes walks the vault collecting .md paths, skipping Obsidian's own
// metadata and anything hidden.
func vaultNotes() []string {
	var notes []string
	filepath.WalkDir(core.VaultPath, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") { // .obsidian, .trash, .git
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".md") {
			notes = append(notes, p)
		}
		return nil
	})
	return notes
}

func toolVaultSearch(s *agent.Sandbox, a agent.ToolArgs) string {
	query := strings.TrimSpace(a.Str("query"))
	if query == "" {
		return "ERROR: query must not be empty"
	}
	q := strings.ToLower(query)
	const maxNotes, maxLines = 15, 40
	var b strings.Builder
	notesHit, linesHit := 0, 0
	for _, p := range vaultNotes() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(core.VaultPath, p)
		hit := false
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), q) {
				if !hit {
					if notesHit == maxNotes {
						b.WriteString("...[more notes match — narrow the query]\n")
						return b.String()
					}
					notesHit++
					hit = true
					b.WriteString("## " + strings.TrimSuffix(rel, ".md") + "\n")
				}
				if linesHit < maxLines {
					linesHit++
					b.WriteString(fmt.Sprintf("  L%d: %s\n", i+1, strings.TrimSpace(line)))
				}
			}
		}
	}
	// Semantic section: chunks related by MEANING, for the queries where
	// the user remembers the concept but not the keyword.
	if core.EmbedModel != "" {
		if hits, err := semanticVaultHits(query, 4); err != nil {
			b.WriteString(fmt.Sprintf("(semantic search unavailable: %v)\n", err))
		} else if len(hits) > 0 {
			b.WriteString("\n## Related by meaning\n")
			for _, h := range hits {
				snippet := strings.ReplaceAll(h.chunk.Text, "\n", " ")
				if len(snippet) > 160 {
					snippet = snippet[:160] + "…"
				}
				b.WriteString(fmt.Sprintf("  %s (%.2f): %s\n", strings.TrimSuffix(h.chunk.Path, ".md"), h.score, snippet))
			}
		}
	}
	if b.Len() == 0 {
		return "(no notes match — try a shorter or different term)"
	}
	if notesHit == 0 {
		return "(no keyword matches)\n" + b.String()
	}
	return b.String()
}

func toolVaultRead(s *agent.Sandbox, a agent.ToolArgs) string {
	name := strings.TrimSpace(strings.TrimSuffix(a.Str("note"), ".md"))
	if name == "" {
		return "ERROR: note name must not be empty"
	}
	// Resolve fuzzily: exact basename > basename contains > path contains.
	type cand struct {
		path  string
		score int
	}
	var cands []cand
	lname := strings.ToLower(name)
	for _, p := range vaultNotes() {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(p), ".md"))
		rel, _ := filepath.Rel(core.VaultPath, p)
		switch {
		case base == lname:
			cands = append(cands, cand{p, 3})
		case strings.Contains(base, lname):
			cands = append(cands, cand{p, 2})
		case strings.Contains(strings.ToLower(rel), lname):
			cands = append(cands, cand{p, 1})
		}
	}
	if len(cands) == 0 {
		return fmt.Sprintf("ERROR: no note matches %q — vault_search can find it by content instead", name)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	if len(cands) > 1 && cands[0].score == cands[1].score {
		var opts []string
		for i, c := range cands {
			if i == 5 {
				break
			}
			rel, _ := filepath.Rel(core.VaultPath, c.path)
			opts = append(opts, strings.TrimSuffix(rel, ".md"))
		}
		return "ERROR: ambiguous — multiple notes match: " + strings.Join(opts, " | ") + ". Use a fuller name."
	}
	data, err := os.ReadFile(cands[0].path)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	rel, _ := filepath.Rel(core.VaultPath, cands[0].path)
	content := string(data)
	const cap = 32 * 1024
	if len(content) > cap {
		content = content[:cap] + "\n...[note truncated at 32KB]"
	}
	out := "# " + strings.TrimSuffix(rel, ".md") + "\n\n" + content
	// Outgoing wikilinks: the model's map of where this topic continues.
	links := map[string]bool{}
	for _, m := range wikilinkRe.FindAllStringSubmatch(content, -1) {
		links[strings.TrimSpace(m[1])] = true
	}
	if len(links) > 0 {
		var ls []string
		for l := range links {
			ls = append(ls, "[["+l+"]]")
		}
		sort.Strings(ls)
		out += "\n\nOutgoing links: " + strings.Join(ls, ", ")
	}
	return out
}

func toolVaultNote(s *agent.Sandbox, a agent.ToolArgs) string {
	title := strings.TrimSpace(strings.TrimSuffix(a.Str("title"), ".md"))
	content := strings.TrimSpace(a.Str("content"))
	mode := strings.TrimSpace(a.Str("mode"))
	if mode == "" {
		mode = "append"
	}
	if title == "" || content == "" {
		return "ERROR: title and content are both required"
	}
	// The sandbox: writes land under <vault>/agent/, full stop. Path
	// separators and traversal in the title are rejected rather than
	// cleaned — a title is a name, not a path.
	if strings.ContainsAny(title, "/\\") || strings.Contains(title, "..") {
		return "ERROR: title must be a plain note name (no path separators) — all agent notes live in the vault's agent/ folder"
	}
	dir := filepath.Join(core.VaultPath, core.VaultAgentDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "ERROR: " + err.Error()
	}
	path := filepath.Join(dir, title+".md")
	_, statErr := os.Stat(path)
	exists := statErr == nil
	switch mode {
	case "create":
		if exists {
			return fmt.Sprintf("ERROR: %s/%s.md already exists — use mode 'append', or a different title", core.VaultAgentDir, title)
		}
		if err := os.WriteFile(path, []byte("# "+title+"\n\n"+content+"\n"), 0o644); err != nil {
			return "ERROR: " + err.Error()
		}
	case "append":
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return "ERROR: " + err.Error()
		}
		section := fmt.Sprintf("\n## %s\n\n%s\n", time.Now().Format("2006-01-02 15:04"), content)
		if !exists {
			section = "# " + title + "\n" + section
		}
		if _, err := f.WriteString(section); err != nil {
			f.Close()
			return "ERROR: " + err.Error()
		}
		f.Close()
	default:
		return "ERROR: mode must be 'append' or 'create'"
	}
	fmt.Println(core.Tint(core.ColorGreen, fmt.Sprintf("  ✏ vault note: %s/%s.md (%s, %d bytes)", core.VaultAgentDir, title, mode, len(content))))
	return fmt.Sprintf("OK: %s %s/%s.md in the vault", mode, core.VaultAgentDir, title)
}
