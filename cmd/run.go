package cmd

import (
	"github.com/spf13/cobra"

	"github.com/davasorus/computah/internal/agent"
)

var (
	runResume string
	runTUI    bool
)

var runCmd = &cobra.Command{
	Use:   "run [workdir]",
	Short: "Start the interactive agent (default command)",
	Long: `Start the interactive agent REPL in the given working directory
(default: current directory). This is the default command, so 'computah' and
'computah run' are equivalent.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := baseOptions()
		opts.Resume = runResume
		opts.Tui = runTUI
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
	runCmd.Flags().StringVar(&runResume, "resume", "",
		"resume a session: 'latest', 'pick', or a name from /sessions")
	runCmd.Flags().BoolVar(&runTUI, "tui", false,
		"run the full-screen TUI instead of the plain REPL (experimental)")
}
