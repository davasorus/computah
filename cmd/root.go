// Package cmd defines computah's Cobra command tree. Commands are thin: they
// parse flags (via Cobra) and configuration (via Viper), assemble an
// agent.Options, and hand off to internal/agent. No agent logic lives here.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/davasorus/computah/internal/agent"
)

// persistent flags shared by all commands
var (
	flagURL   string
	flagModel string
)

var rootCmd = &cobra.Command{
	Use:   "computah",
	Short: "A self-hosted terminal AI coding agent (local LM Studio / OpenAI-compatible)",
	Long: `computah is a terminal-based AI coding agent that runs against a local
OpenAI-compatible model server such as LM Studio. It reads and edits code in a
working directory, runs commands with approval, keeps resumable sessions, and
can extend itself with MCP tool servers.

Run with no subcommand to start the interactive agent in the current directory.`,
	// Bare `computah` == `computah run`.
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCmd.RunE(cmd, args)
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute is the process entry point, called by main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if handleExit(err) {
			return // handleExit called os.Exit with the agent's code
		}
		fmt.Fprintln(os.Stderr, "computah:", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Persistent flags available to every subcommand. Bound to Viper so a
	// config file or COMPUTAH_* env var can supply them too.
	rootCmd.PersistentFlags().StringVar(&flagURL, "url", "",
		"base URL of the OpenAI-compatible server (default: auto-detect / config)")
	rootCmd.PersistentFlags().StringVar(&flagModel, "model", "",
		"model id (default: first chat model on the server / config)")
	_ = viper.BindPFlag("url", rootCmd.PersistentFlags().Lookup("url"))
	_ = viper.BindPFlag("model", rootCmd.PersistentFlags().Lookup("model"))

	rootCmd.AddCommand(runCmd, execCmd, evalCmd, dashboardCmd, versionCmd)
}

// initConfig wires Viper: it reads an optional config file and COMPUTAH_* env
// vars. This supplements (does not replace) the agent's own ~/.agent/config.json
// loading — Viper covers the CLI-surface settings (url, model) so they can come
// from flag, env, or a computah config file, with flags taking precedence.
func initConfig() {
	viper.SetEnvPrefix("COMPUTAH")
	viper.AutomaticEnv()

	if home, err := os.UserHomeDir(); err == nil {
		viper.AddConfigPath(home + "/.agent")
		viper.SetConfigName("computah") // ~/.agent/computah.yaml|json|toml (optional)
		viper.SetConfigType("yaml")
		_ = viper.ReadInConfig() // absent config is fine
	}
}

// resolvedURL / resolvedModel return the CLI-surface values, letting Viper
// merge flag > env > config-file. Empty means "let the agent auto-detect",
// preserving the agent's existing resolution chain.
func resolvedURL() string   { return viper.GetString("url") }
func resolvedModel() string { return viper.GetString("model") }

// baseOptions seeds an agent.Options from the persistent (root) flags.
func baseOptions() agent.Options {
	return agent.Options{URL: resolvedURL(), Model: resolvedModel(), Runs: 1}
}
