// record_decision — a single tool call that writes a well-structured,
// frontmatter-tagged knowledge note into the vault.
//
// Why this exists: watching the agent try to "update the roadmap," it burned
// minutes fumbling note structure — which path, which heading, what
// frontmatter. This tool removes that reasoning entirely: the STRUCTURE is
// baked into the tool, so the model supplies only the content
// (title/type/project/tags/body) and gets a correctly-formatted note filed
// under agent/decisions/ with Dataview-queryable frontmatter matching the
// vault protocol convention.
//
// It writes to the vault filesystem directly (like vault_note), so it works
// whether or not the MCP vault server is connected — and it is NOT a
// duplicate of any MCP tool (nothing in the Obsidian MCP suite encodes this
// convention), so it survives the file-layer dedup.
package toolsext

import (
	"fmt"
	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RegisterDecisionTool adds record_decision when a vault is configured.
func RegisterDecisionTool() {
	if core.VaultPath == "" {
		return
	}
	agent.RegisterTools(agent.Tool{
		Name: "record_decision",
		Desc: "Record a durable decision, gotcha, architecture note, or RCA into the knowledge vault as a structured, queryable note. Use when finishing significant work whose conclusion should outlive this session. Supply the content; the tool handles filing and frontmatter. Prefer this over vault_write for decisions — it enforces the team convention.",
		Props: map[string]any{
			"title":   map[string]any{"type": "string", "description": "Short title, e.g. 'Why PromoteIS needs GUID convergence'"},
			"type":    map[string]any{"type": "string", "enum": []string{"decision", "gotcha", "architecture", "rca", "reference"}, "description": "The kind of note"},
			"project": map[string]any{"type": "string", "description": "Project slug, e.g. 'proj9' or 'ims-promote'"},
			"tags":    map[string]any{"type": "string", "description": "Comma-separated topic tags, e.g. 'sql,guid,promoteis'"},
			"body":    map[string]any{"type": "string", "description": "The note body in markdown: conclusion first, then reasoning. One idea per note."},
		},
		Required: []string{"title", "type", "body"},
		Handler:  toolRecordDecision,
	})
	agent.RebuildToolSchemas()
}

func toolRecordDecision(s *agent.Sandbox, a agent.ToolArgs) string {
	title := strings.TrimSpace(a.Str("title"))
	noteType := strings.TrimSpace(a.Str("type"))
	project := strings.TrimSpace(a.Str("project"))
	tagsRaw := strings.TrimSpace(a.Str("tags"))
	body := a.Str("body")

	if title == "" || body == "" {
		return "ERROR: title and body are required"
	}
	if noteType == "" {
		noteType = "decision"
	}
	switch noteType {
	case "decision", "gotcha", "architecture", "rca", "reference":
	default:
		return "ERROR: type must be one of: decision, gotcha, architecture, rca, reference"
	}

	// Build the tag list: always include the type; add project and any
	// user-supplied tags, de-duplicated.
	tagSet := []string{noteType}
	seen := map[string]bool{noteType: true}
	addTag := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		tagSet = append(tagSet, t)
	}
	if project != "" {
		addTag(project)
	}
	for _, t := range strings.Split(tagsRaw, ",") {
		addTag(t)
	}

	now := time.Now()
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "type: %s\n", noteType)
	if project != "" {
		fmt.Fprintf(&b, "project: %s\n", project)
	}
	fmt.Fprintf(&b, "date: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(tagSet, ", "))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n")

	// File under agent/decisions/ with a slugged, dated filename.
	dir := filepath.Join(core.VaultPath, core.VaultAgentDir, "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "ERROR: cannot create decisions dir: " + err.Error()
	}
	fname := now.Format("2006-01-02") + "-" + slug(title) + ".md"
	full := filepath.Join(dir, fname)
	if _, err := os.Stat(full); err == nil {
		// Avoid clobbering: append a time suffix.
		fname = now.Format("2006-01-02-150405") + "-" + slug(title) + ".md"
		full = filepath.Join(dir, fname)
	}
	if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
		return "ERROR: write failed: " + err.Error()
	}
	rel := filepath.Join(core.VaultAgentDir, "decisions", fname)
	core.EmitLine("  ✏ recorded " + noteType + ": " + rel)
	return "Recorded " + noteType + " note at " + rel + " with frontmatter (type, project, date, tags). It is now Dataview-queryable."
}

// slug turns a title into a filesystem-safe, lowercase, hyphenated stem.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}
	if out == "" {
		out = "note"
	}
	return out
}
