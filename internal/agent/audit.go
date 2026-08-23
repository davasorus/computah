// Session activity → a structured Obsidian audit note.
//
// This is monitoring for the HUMAN, not the model: none of it enters the
// context window (zero token cost). Every tool call passes through
// Sandbox.Execute, which is the one chokepoint where we tally activity —
// counts by tool, vault notes touched, commands run, files modified. At
// session end writeAuditNote drains the tally into a dated, frontmatter-
// tagged note under agent/audit/ so you can review — and Dataview-query —
// exactly what the agent did, in the same vault you already browse.
//
// Gated by config "audit" (needs a vault). Independent of "journal" (the
// prose summary); you can run either, both, or neither.
package agent

import (
	"fmt"
	"github.com/davasorus/computah/internal/core"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// auditEnabled gates the audit note (config "audit").
var auditEnabled bool

// activity is the in-memory session tally. Guarded because read-only tools
// run concurrently in the turn loop.
var activity = struct {
	sync.Mutex
	started    time.Time
	toolCounts map[string]int
	vaultNotes []string // vault note paths created/patched this session
	commands   []string // shell commands run (first ~60 chars each)
	errors     int      // tool calls that returned an ERROR
}{toolCounts: map[string]int{}}

func recordActivity(tool string, args map[string]any, result string) {
	activity.Lock()
	defer activity.Unlock()
	if activity.started.IsZero() {
		activity.started = time.Now()
	}
	activity.toolCounts[tool]++
	if strings.HasPrefix(result, "ERROR") {
		activity.errors++
	}
	switch tool {
	case "vault_note", "vault_patch", "obsidian_patch":
		if p, _ := args["path"].(string); p != "" {
			activity.vaultNotes = append(activity.vaultNotes, p)
		} else if t, _ := args["title"].(string); t != "" {
			activity.vaultNotes = append(activity.vaultNotes, t)
		}
	case "run_command":
		if c, _ := args["command"].(string); c != "" {
			activity.commands = append(activity.commands, clip(c, 60))
		}
	}
}

// writeAuditNote emits the structured audit note. Best-effort like the
// journal: never blocks exit, never errors loudly.
func writeAuditNote(workdir, title string, sb *Sandbox) {
	if core.VaultPath == "" || !auditEnabled {
		return
	}
	activity.Lock()
	defer activity.Unlock()
	total := 0
	for _, n := range activity.toolCounts {
		total += n
	}
	if total == 0 {
		return // nothing happened; no note
	}

	dir := filepath.Join(core.VaultPath, core.VaultAgentDir, "audit")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	now := time.Now()
	dur := time.Duration(0)
	if !activity.started.IsZero() {
		dur = now.Sub(activity.started).Round(time.Second)
	}
	project := filepath.Base(workdir)

	var b strings.Builder
	// Frontmatter — this is what makes the audit trail Dataview-queryable:
	// "table from #agent-audit where project = 'proj9'".
	b.WriteString("---\n")
	b.WriteString("type: agent-audit\n")
	fmt.Fprintf(&b, "project: %s\n", project)
	fmt.Fprintf(&b, "date: %s\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "time: %s\n", now.Format("15:04"))
	fmt.Fprintf(&b, "model: %s\n", curModel)
	fmt.Fprintf(&b, "tool_calls: %d\n", total)
	fmt.Fprintf(&b, "files_modified: %d\n", len(core.Dedup(core.RelPaths(sb.Root, sb.Modified))))
	fmt.Fprintf(&b, "errors: %d\n", activity.errors)
	fmt.Fprintf(&b, "duration_min: %d\n", int(dur.Minutes()))
	b.WriteString("tags: [agent-audit]\n")
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# Agent session — %s — %s\n\n", project, now.Format("2006-01-02 15:04"))
	if title != "" {
		fmt.Fprintf(&b, "**%s**\n\n", title)
	}

	// Tool usage, most-used first.
	b.WriteString("## Tool usage\n")
	type tc struct {
		name string
		n    int
	}
	var tcs []tc
	for name, n := range activity.toolCounts {
		tcs = append(tcs, tc{name, n})
	}
	sort.Slice(tcs, func(i, j int) bool { return tcs[i].n > tcs[j].n })
	for _, t := range tcs {
		fmt.Fprintf(&b, "- %s ×%d\n", t.name, t.n)
	}

	if mods := core.Dedup(core.RelPaths(sb.Root, sb.Modified)); len(mods) > 0 {
		b.WriteString("\n## Files modified\n")
		for _, m := range mods {
			fmt.Fprintf(&b, "- `%s`\n", m)
		}
	}
	if len(activity.vaultNotes) > 0 {
		b.WriteString("\n## Vault notes touched\n")
		for _, n := range core.Dedup(activity.vaultNotes) {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	if len(activity.commands) > 0 {
		b.WriteString("\n## Commands run\n")
		for _, c := range activity.commands {
			fmt.Fprintf(&b, "- `%s`\n", c)
		}
	}

	fname := fmt.Sprintf("%s-%s-%s.md", now.Format("2006-01-02-150405"), project, "audit")
	p := filepath.Join(dir, fname)
	if os.WriteFile(p, []byte(b.String()), 0o644) == nil {
		fmt.Printf("audit note written to vault: %s\n", filepath.Join(core.VaultAgentDir, "audit", fname))
	}
}
