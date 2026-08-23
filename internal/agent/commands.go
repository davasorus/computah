// Custom slash commands — prompt templates as files.
//
// Your recurring runbook-style prompts become commands: a markdown file at
// <repo>/.agent/commands/<name>.md (or ~/.agent/commands/<name>.md for
// personal ones) turns into /<name>. The file body is the prompt; the
// placeholder $ARGUMENTS is replaced with whatever follows the command:
//
//	.agent/commands/migrate.md:
//	  Migrate $ARGUMENTS to the reusable build workflow. Read the current
//	  workflow first, keep OTCOM versioning intact, and verify with a dry run.
//
//	> /migrate proj5
//
// Repo-local commands shadow personal ones of the same name. Built-in
// commands always win — a template can't override /verify. Discovered at
// startup; shown in /help and Tab-completed like any other command.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// customCommands maps name (without slash) → template body.
var customCommands = map[string]string{}

// loadCustomCommands scans personal then repo-local command dirs; repo-local
// wins on name collision (loaded second, overwrites).
func loadCustomCommands(root string) {
	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".agent", "commands"))
	}
	dirs = append(dirs, filepath.Join(root, ".agent", "commands"))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if isBuiltinCommand(name) {
				fmt.Printf("(custom command /%s ignored — shadows a built-in)\n", name)
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || strings.TrimSpace(string(data)) == "" {
				continue
			}
			customCommands[name] = strings.TrimSpace(string(data))
		}
	}
	if len(customCommands) > 0 {
		var names []string
		for n := range customCommands {
			names = append(names, "/"+n)
			replCommands = append(replCommands, "/"+n) // Tab completion
		}
		sort.Strings(names)
		fmt.Println("custom commands:", strings.Join(names, " "))
	}
}

func isBuiltinCommand(name string) bool {
	for _, c := range replCommands {
		if c == "/"+name {
			return true
		}
	}
	return false
}

// expandCustomCommand resolves "/name args" into the template with
// $ARGUMENTS substituted. Returns ("", false) when it's not a custom command.
func expandCustomCommand(input string) (string, bool) {
	if !strings.HasPrefix(input, "/") {
		return "", false
	}
	name, args, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
	tmpl, ok := customCommands[name]
	if !ok {
		return "", false
	}
	args = strings.TrimSpace(args)
	if strings.Contains(tmpl, "$ARGUMENTS") && args == "" {
		return "", false // template wants arguments; let the caller complain
	}
	return strings.ReplaceAll(tmpl, "$ARGUMENTS", args), true
}
