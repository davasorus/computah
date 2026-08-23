package cmd

import (
	"github.com/spf13/cobra"

	"github.com/davasorus/computah/internal/agent"
)

var (
	evalRuns  int
	evalForce bool
)

var evalCmd = &cobra.Command{
	Use:   "eval <file> [workdir]",
	Short: "Run an eval file and report pass rates",
	Long: `Run an eval file (a JSON array of {name, prompt, check}) against the
agent and report pass rates. Use --runs to repeat each case and --force to skip
the dirty-tree and baseline-build preflight checks.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := baseOptions()
		opts.Eval = args[0]
		opts.Runs = evalRuns
		opts.ForceEval = evalForce
		if len(args) == 2 {
			opts.PositionalRoot = args[1]
		}
		if code := agent.Run(opts); code != 0 {
			return exitError(code)
		}
		return nil
	},
}

func init() {
	evalCmd.Flags().IntVar(&evalRuns, "runs", 1, "runs per eval case")
	evalCmd.Flags().BoolVar(&evalForce, "force", false,
		"skip eval preflight (dirty-tree and baseline-build checks)")
}
