// Named agent roles + auxiliary model routing.
//
// ROLES — .agent/agents/<name>.md (repo) or ~/.agent/agents/<name>.md
// (personal; repo shadows personal) define specialists the model can
// delegate to via spawn_task's "role" argument:
//
//	.agent/agents/reviewer.md:
//	  model: qwen2.5-coder-14b-instruct
//
//	  You are a strict code reviewer. Point out bugs, race conditions, and
//	  deviations from the repo's conventions. Do not fix anything — report.
//
// The optional "model: <id>" header routes the subtask to a different model
// on the same server; the rest of the file is appended to the subtask's
// system prompt. /agents lists what's loaded.
//
// AUX MODEL — "aux_model" in config routes housekeeping (compaction
// summaries, session titles, commit messages) to a smaller/faster model.
// None of those need the 12B, and every VRAM-second they don't take is one
// the main model gets.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type agentRole struct {
	Name   string
	Model  string // optional; empty = the session's main model
	Prompt string // appended to the subtask system prompt
}

var agentRoles = map[string]agentRole{}

// auxModel is the housekeeping model (config "aux_model"); empty = main.
var auxModel string

// auxModelFor returns the model for background/housekeeping calls.
func auxModelFor() string {
	if auxModel != "" {
		return auxModel
	}
	return curModel
}

// modelForTurn returns the model a turn should use. Precedence:
//  1. Plan mode → planModel (planning is reasoning-heavy; never "trivial").
//  2. A trivial follow-up turn → fastModel (cheap/fast model for the common
//     "yes" / "continue" / "now do X" turns that don't need the big model).
//  3. Otherwise → the main model.
//
// Each tier falls back to curModel when its model isn't configured, so the
// default (nothing set) is unchanged: every turn uses the main model.
func modelForTurn(userInput string) string {
	if planMode {
		if planModel != "" {
			return planModel
		}
		return curModel
	}
	if fastModel != "" && isTrivialTurn(userInput) {
		return fastModel
	}
	return curModel
}

// isTrivialTurn is a deliberately CONSERVATIVE heuristic: it returns true only
// for turns that are obviously simple follow-ups, so misrouting a hard turn to
// a weak model is rare. It classifies by the SURFACE of the request (short,
// or a known continuation phrase) rather than trying to understand it — which
// would need an extra model call and defeat the point. When unsure, it returns
// false and the main model handles the turn.
func isTrivialTurn(input string) bool {
	s := strings.ToLower(strings.TrimSpace(input))
	if s == "" {
		return false
	}
	// Multi-line or long inputs are never treated as trivial — a wall of text
	// or a pasted spec is real work.
	if strings.Contains(s, "\n") || len(s) > 80 {
		return false
	}
	// Exact-match continuations: the classic cheap follow-ups.
	switch s {
	case "yes", "y", "yep", "yeah", "ok", "okay", "sure", "go", "go ahead",
		"continue", "proceed", "do it", "next", "please continue", "keep going",
		"no", "n", "stop", "thanks", "thank you":
		return true
	}
	// Short imperative follow-ups that lean on the model's just-built context.
	// Require BOTH a continuation lead-in AND brevity, so "commit that" routes
	// fast but "refactor the auth layer to use interfaces" does not.
	trivialLeads := []string{
		"commit", "run the tests", "run tests", "push", "try again",
		"fix that", "fix it", "undo that", "show me", "list ", "explain that",
	}
	for _, lead := range trivialLeads {
		if strings.HasPrefix(s, lead) {
			return true
		}
	}
	return false
}

// loadAgentRoles scans personal then repo-local role dirs; repo wins.
func loadAgentRoles(root string) {
	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".agent", "agents"))
	}
	dirs = append(dirs, filepath.Join(root, ".agent", "agents"))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			role := parseAgentRole(strings.TrimSuffix(e.Name(), ".md"), string(data))
			if role.Prompt == "" {
				continue
			}
			agentRoles[role.Name] = role
		}
	}
	if len(agentRoles) > 0 {
		var names []string
		for n := range agentRoles {
			names = append(names, n)
		}
		sort.Strings(names)
		emitLine("agent roles: " + strings.Join(names, " ") + " — usable via spawn_task or /agents")
	}
}

// parseAgentRole reads an optional "model: <id>" header line, then treats
// the rest of the file as the role prompt.
func parseAgentRole(name, body string) agentRole {
	role := agentRole{Name: name}
	lines := strings.Split(body, "\n")
	i := 0
	for ; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if m, ok := strings.CutPrefix(l, "model:"); ok {
			role.Model = strings.TrimSpace(m)
			continue
		}
		break // first non-header, non-blank line starts the prompt
	}
	role.Prompt = strings.TrimSpace(strings.Join(lines[i:], "\n"))
	return role
}

func listAgentRoles() {
	if len(agentRoles) == 0 {
		emitLine("no agent roles defined — add .agent/agents/<name>.md (see file header of roles.go)")
		return
	}
	var names []string
	for n := range agentRoles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := agentRoles[n]
		model := r.Model
		if model == "" {
			model = "(main model)"
		}
		emitLine(fmt.Sprintf("  %-14s %-30s %s", n, model, firstSentence(r.Prompt)))
	}
	emitLine(`the model delegates with spawn_task {"role": "<name>", "task": "..."}`)
}
