package cmd

import (
	"github.com/spf13/cobra"

	"github.com/davasorus/computah/internal/agent"
)

var (
	dashAddr     string
	dashWrite    bool
	dashHeadless bool
	dashResume   string
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard [workdir]",
	Short: "Serve the web dashboard (read-only, two-way, or headless)",
	Long: `Serve the web dashboard. By default it is read-only. With --write the
dashboard can submit prompts back to the agent (two-way). With --headless there
is no terminal UI at all — the dashboard is the only interface.

--addr controls the listen address (default :7777); accepts ':PORT' or 'PORT'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := baseOptions()
		opts.Serve = dashAddr
		if opts.Serve == "" {
			opts.Serve = "on" // presence of the subcommand means "serve"
		}
		opts.ServeWrite = dashWrite
		opts.Headless = dashHeadless
		opts.Resume = dashResume
		if len(args) == 1 {
			opts.PositionalRoot = args[0]
		}
		if code := agent.Run(opts); code != 0 {
			return exitError(code)
		}
		return nil
	},
}

func init() {
	dashboardCmd.Flags().StringVar(&dashAddr, "addr", "",
		"listen address for the dashboard (default :7777)")
	dashboardCmd.Flags().BoolVar(&dashWrite, "write", false,
		"allow the dashboard to submit prompts (two-way)")
	dashboardCmd.Flags().BoolVar(&dashHeadless, "headless", false,
		"no terminal UI: drive the agent solely from the dashboard (implies --write)")
	dashboardCmd.Flags().StringVar(&dashResume, "resume", "",
		"resume a session: 'latest', 'pick', or a name")
}
