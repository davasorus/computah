// Command tools — user-defined tools without recompiling.
//
// MCP is the heavyweight plugin path (a running server, its own protocol);
// this is the lightweight one: drop a manifest in .agent/tools/<name>.json
// describing a tool and the executable that backs it, and it joins the
// registry at startup. Wrap a PowerShell deploy script, a curl one-liner, a
// Python analyzer — anything runnable — as a first-class tool the model can
// call, with typed arguments it can't typo.
//
//	.agent/tools/deploy_status.json
//	{
//	  "name": "deploy_status",
//	  "description": "Check the deploy status of a service in an environment.",
//	  "command": ["pwsh", "-File", "scripts/DeployStatus.ps1"],
//	  "parameters": {
//	    "service": {"type": "string", "description": "Service name", "required": true},
//	    "env":     {"type": "string", "description": "dev|stage|prod"}
//	  },
//	  "read_only": true,
//	  "timeout_sec": 60
//	}
//
// The model's typed arguments are passed to the executable two ways, so the
// script can consume whichever is convenient: as --key value flags appended
// to the command, AND as a JSON object on stdin. The command runs in the
// workdir; stdout+stderr (capped) come back as the tool result.
//
// Safety: read_only tools skip the approval prompt and may run in plan mode
// (declare it only for genuinely side-effect-free scripts). Everything else
// goes through the same y/N approval as run_command. Manifests are the
// user's own files under their repo — trusted like a Makefile, not like
// model output.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/core"
)

type cmdToolParam struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type cmdToolManifest struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Command     []string                `json:"command"` // argv; parameters appended as --key value
	Parameters  map[string]cmdToolParam `json:"parameters"`
	ReadOnly    bool                    `json:"read_only"`
	TimeoutSec  int                     `json:"timeout_sec"`
}

// loadCommandTools scans personal then repo-local tool dirs; repo wins on a
// name clash, and neither may shadow a built-in tool.
func loadCommandTools(root string) {
	dirs := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".agent", "tools"))
	}
	dirs = append(dirs, filepath.Join(root, ".agent", "tools"))
	loaded := map[string]bool{}
	var names []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var m cmdToolManifest
			if err := json.Unmarshal(data, &m); err != nil {
				core.EmitStatus("command tool " + core.LogSafe(e.Name()) + ": bad manifest (" + fmt.Sprint(err) + ") — skipped")
				continue
			}
			if m.Name == "" || len(m.Command) == 0 {
				core.EmitStatus("command tool " + core.LogSafe(e.Name()) + ": needs a name and a non-empty command — skipped")
				continue
			}
			if _, isBuiltin := toolByName[m.Name]; isBuiltin && !loaded[m.Name] {
				core.EmitStatus("command tool " + core.LogSafe(m.Name) + " ignored — shadows a built-in tool")
				continue
			}
			registerTools(makeCommandTool(m, root))
			if m.ReadOnly {
				readOnlyTools[m.Name] = true
			}
			if !loaded[m.Name] {
				names = append(names, m.Name)
			}
			loaded[m.Name] = true
		}
	}
	if len(names) > 0 {
		buildToolSchemas()
		sort.Strings(names)
		core.EmitStatus("command tools: " + strings.Join(names, " "))
	}
}

// makeCommandTool turns a manifest into a registry Tool.
func makeCommandTool(m cmdToolManifest, root string) Tool {
	props := map[string]any{}
	var required []string
	for pname, p := range m.Parameters {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		props[pname] = map[string]any{"type": typ, "description": p.Description}
		if p.Required {
			required = append(required, pname)
		}
	}
	desc := m.Description
	if desc == "" {
		desc = "User-defined command tool: " + m.Name
	}
	timeout := commandTimeout
	if m.TimeoutSec > 0 {
		timeout = time.Duration(m.TimeoutSec) * time.Second
	}
	return Tool{
		Name:     m.Name,
		Desc:     desc,
		Props:    props,
		Required: required,
		Handler: func(s *Sandbox, args toolArgs) string {
			return runCommandTool(s, m, root, timeout, args)
		},
	}
}

func runCommandTool(s *Sandbox, m cmdToolManifest, root string, timeout time.Duration, args toolArgs) string {
	// Validate required parameters up front.
	for pname, p := range m.Parameters {
		if p.Required {
			if _, ok := args[pname]; !ok {
				return fmt.Sprintf("ERROR: %s requires parameter %q", m.Name, pname)
			}
		}
	}
	// Build argv: manifest command + --key value for each provided param.
	argv := append([]string{}, m.Command...)
	// Deterministic flag order so calls are reproducible / cache-friendly.
	var keys []string
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, declared := m.Parameters[k]; !declared {
			continue // ignore args the manifest doesn't declare
		}
		argv = append(argv, "--"+k, fmt.Sprintf("%v", args[k]))
	}

	// Non-read-only command tools need approval, same gate as run_command.
	display := strings.Join(argv, " ")
	if !m.ReadOnly {
		core.EmitStatus("  ⚙ " + core.LogSafe(m.Name) + ": " + core.LogSafe(display))
		notifyApproval("run command tool: " + m.Name)
		if approvals.request("    run this command tool? [y/N] ", m.Name) == approveDeny {
			return "User declined to run the command tool " + m.Name + "."
		}
	} else {
		core.EmitStatus("  ⚙ " + core.LogSafe(m.Name) + " (read-only)")
	}

	// Provide args as JSON on stdin as well, for scripts that prefer it.
	stdinJSON, _ := json.Marshal(map[string]any(args))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(stdinJSON)
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("ERROR: %s timed out after %s. Partial output:\n%s", m.Name, timeout, tail(result, 2048))
	}
	if err != nil {
		return fmt.Sprintf("%s exited with error (%v):\n%s", m.Name, err, tail(result, 4096))
	}
	if result == "" {
		return fmt.Sprintf("OK: %s completed (no output).", m.Name)
	}
	return tail(result, 8192)
}
