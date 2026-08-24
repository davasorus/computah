// config.go adds `computah config ...` — inspect and edit ~/.agent/config.json
// without hand-editing JSON. All persistence goes through the agent's
// ConfigPath / LoadConfigFrom / SaveConfig helpers so this file stays a thin
// Cobra wiring layer (no config logic lives here). SaveConfig backs the file
// up when a rewrite would drop keys the struct doesn't model.
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/davasorus/computah/internal/agent"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and edit ~/.agent/config.json",
	Long: "Inspect and edit the agent config (~/.agent/config.json).\n\n" +
		"Writes go through a typed round-trip: the file is rewritten as clean,\n" +
		"indented JSON. If it contains keys the agent doesn't model (e.g.\n" +
		"\"// comment\" keys), a <path>.bak backup is made first so nothing is lost.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the config file path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := agent.ConfigPath()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the current effective config as JSON",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, found, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		if !found {
			p, _ := agent.ConfigPath()
			fmt.Fprintf(os.Stderr, "no config at %s (using defaults); run `computah config init`\n", p)
		}
		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	},
}

var configInitForce bool

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a starter config file",
	Long:  "Create ~/.agent/config.json with a minimal starter. Refuses to overwrite an existing file unless --force.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := agent.ConfigPath()
		if err != nil {
			return err
		}
		if _, found, _ := agent.LoadConfigFrom(); found && !configInitForce {
			return fmt.Errorf("%s already exists; use --force to overwrite (a .bak is kept)", p)
		}
		// Minimal, sensible starter: point at a common local server port.
		starter := agent.Config{
			URL: "http://localhost:1234/v1",
		}
		backup, err := agent.SaveConfig(starter)
		if err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", p)
		if backup != "" {
			fmt.Printf("backed up previous config to %s\n", backup)
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Print a single config value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		// Round-trip through JSON so the key names match the file exactly.
		out, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		v, ok := m[args[0]]
		if !ok {
			return fmt.Errorf("key %q is unset (using its default)", args[0])
		}
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a scalar config value",
	Long: "Set a scalar (string/number/bool) config field by its JSON key, e.g.\n" +
		"  computah config set model my-model\n" +
		"  computah config set max_tokens 16384\n" +
		"  computah config set journal true\n\n" +
		"Structured fields (hooks, mcp_servers) have their own subcommands.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, val := args[0], args[1]
		cfg, _, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		// Merge by editing the map form, then re-decoding into the struct so
		// the value lands in the right typed field (and bad keys are caught).
		blob, _ := json.Marshal(cfg)
		var m map[string]any
		_ = json.Unmarshal(blob, &m)

		typed, perr := coerce(val)
		if perr != nil {
			return perr
		}
		m[key] = typed
		merged, _ := json.Marshal(m)
		var check agent.Config
		dec := json.NewDecoder(strings.NewReader(string(merged)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&check); err != nil {
			return fmt.Errorf("invalid key or value for %q: %w", key, err)
		}
		backup, err := agent.SaveConfig(check)
		if err != nil {
			return err
		}
		fmt.Printf("set %s = %s\n", key, val)
		if backup != "" {
			fmt.Printf("backed up previous config to %s\n", backup)
		}
		return nil
	},
}

// coerce turns a CLI string into the most natural JSON type: bool, number, or
// string. This lets `set max_tokens 16384` store a number, not "16384".
func coerce(s string) (any, error) {
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if i, err := strconv.Atoi(s); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	return s, nil
}

// ── MCP subcommands ─────────────────────────────────────────────────────────

var (
	mcpCommand string
	mcpArgs    []string
	mcpURL     string
	mcpToken   string
	mcpPrefer  bool
	mcpHint    string
	mcpNoPfx   bool
)

var configAddMCPCmd = &cobra.Command{
	Use:   "add-mcp <name>",
	Short: "Add or replace an MCP server",
	Long: "Add an MCP tool server. Use --command/--arg for a stdio server or\n" +
		"--url/--token for an HTTP server.\n\n" +
		"  computah config add-mcp sandbox --command sandbox --arg mcp --arg -image --arg python:3-alpine --prefer \\\n" +
		"    --prefer-hint \"Run untrusted code here, not run_command.\"\n\n" +
		"  computah config add-mcp remote --url https://mcp.example.com/sse --token TOKEN",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if mcpCommand == "" && mcpURL == "" {
			return fmt.Errorf("provide --command (stdio) or --url (http)")
		}
		if mcpCommand != "" && mcpURL != "" {
			return fmt.Errorf("--command and --url are mutually exclusive")
		}
		cfg, _, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]agent.MCPServerConfig{}
		}
		srv := agent.MCPServerConfig{
			Command:    mcpCommand,
			Args:       mcpArgs,
			URL:        mcpURL,
			Token:      mcpToken,
			Prefer:     mcpPrefer,
			PreferHint: mcpHint,
			NoPrefix:   mcpNoPfx,
		}
		_, replacing := cfg.MCPServers[name]
		cfg.MCPServers[name] = srv
		backup, err := agent.SaveConfig(cfg)
		if err != nil {
			return err
		}
		verb := "added"
		if replacing {
			verb = "replaced"
		}
		fmt.Printf("%s MCP server %q\n", verb, name)
		if backup != "" {
			fmt.Printf("backed up previous config to %s\n", backup)
		}
		return nil
	},
}

var configRemoveMCPCmd = &cobra.Command{
	Use:   "remove-mcp <name>",
	Short: "Remove an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, _, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		if _, ok := cfg.MCPServers[name]; !ok {
			return fmt.Errorf("no MCP server named %q", name)
		}
		delete(cfg.MCPServers, name)
		backup, err := agent.SaveConfig(cfg)
		if err != nil {
			return err
		}
		fmt.Printf("removed MCP server %q\n", name)
		if backup != "" {
			fmt.Printf("backed up previous config to %s\n", backup)
		}
		return nil
	},
}

// ── Hook subcommands ────────────────────────────────────────────────────────

var configSetHookCmd = &cobra.Command{
	Use:   "set-hook <post_edit|pre_command|post_turn> <command>",
	Short: "Set a lifecycle hook",
	Long: "Set a shell hook. {file} (post_edit) and {cmd} (pre_command) are\n" +
		"substituted; a nonzero pre_command exit BLOCKS the command.\n\n" +
		"  computah config set-hook post_edit \"gofmt -w {file}\"",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, command := args[0], args[1]
		switch name {
		case "post_edit", "pre_command", "post_turn":
		default:
			return fmt.Errorf("unknown hook %q (want post_edit, pre_command, or post_turn)", name)
		}
		cfg, _, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		if cfg.Hooks == nil {
			cfg.Hooks = map[string]string{}
		}
		cfg.Hooks[name] = command
		backup, err := agent.SaveConfig(cfg)
		if err != nil {
			return err
		}
		fmt.Printf("set hook %s = %q\n", name, command)
		if backup != "" {
			fmt.Printf("backed up previous config to %s\n", backup)
		}
		return nil
	},
}

var configRemoveHookCmd = &cobra.Command{
	Use:   "remove-hook <name>",
	Short: "Remove a lifecycle hook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, _, err := agent.LoadConfigFrom()
		if err != nil {
			return err
		}
		if _, ok := cfg.Hooks[name]; !ok {
			return fmt.Errorf("no hook named %q", name)
		}
		delete(cfg.Hooks, name)
		backup, err := agent.SaveConfig(cfg)
		if err != nil {
			return err
		}
		fmt.Printf("removed hook %q\n", name)
		if backup != "" {
			fmt.Printf("backed up previous config to %s\n", backup)
		}
		return nil
	},
}

func init() {
	configInitCmd.Flags().BoolVar(&configInitForce, "force", false, "overwrite an existing config")

	configAddMCPCmd.Flags().StringVar(&mcpCommand, "command", "", "stdio: executable to run")
	configAddMCPCmd.Flags().StringArrayVar(&mcpArgs, "arg", nil, "stdio: an argument (repeatable)")
	configAddMCPCmd.Flags().StringVar(&mcpURL, "url", "", "http: server URL")
	configAddMCPCmd.Flags().StringVar(&mcpToken, "token", "", "http: bearer token")
	configAddMCPCmd.Flags().BoolVar(&mcpPrefer, "prefer", false, "steer the model toward this server")
	configAddMCPCmd.Flags().StringVar(&mcpHint, "prefer-hint", "", "custom steering text (implies a non-notes server)")
	configAddMCPCmd.Flags().BoolVar(&mcpNoPfx, "no-prefix", false, "register tools under their own names")

	configCmd.AddCommand(
		configPathCmd,
		configShowCmd,
		configInitCmd,
		configGetCmd,
		configSetCmd,
		configAddMCPCmd,
		configRemoveMCPCmd,
		configSetHookCmd,
		configRemoveHookCmd,
	)
}
