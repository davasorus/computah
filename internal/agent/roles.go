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

// modelForTurn returns the model a normal agent turn should use. In plan mode
// it prefers planModel (a stronger model for the reasoning-heavy planning
// step), falling back to the main model when unset — mirroring how
// currentReasoningEffort() prefers planReasoningEffort in plan mode. Execution
// turns (plan mode off) always use curModel.
func modelForTurn() string {
	if planMode && planModel != "" {
		return planModel
	}
	return curModel
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
		fmt.Println("agent roles:", strings.Join(names, " "), "— usable via spawn_task or /agents")
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
		fmt.Println("no agent roles defined — add .agent/agents/<name>.md (see file header of roles.go)")
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
		fmt.Printf("  %-14s %-30s %s\n", n, model, firstSentence(r.Prompt))
	}
	fmt.Println(`the model delegates with spawn_task {"role": "<name>", "task": "..."}`)
}
