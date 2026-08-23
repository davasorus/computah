package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/davasorus/computah/internal/agent"
)

var execYes bool

var execCmd = &cobra.Command{
	Use:   "exec <prompt> [workdir]",
	Short: "Run one prompt non-interactively and exit",
	Long: `Run a single prompt without the interactive REPL and exit with the
agent's status code. Intended for scripting. Use --yes to auto-approve mutating
commands and fetches (otherwise they still prompt).`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := baseOptions()
		opts.Prompt = args[0]
		opts.Yes = execYes
		if len(args) == 2 {
			opts.PositionalRoot = args[1]
		}
		if opts.Prompt == "" {
			return errors.New("exec requires a non-empty prompt")
		}
		if code := agent.Run(opts); code != 0 {
			return exitError(code)
		}
		return nil
	},
}

func init() {
	execCmd.Flags().BoolVar(&execYes, "yes", false,
		"auto-approve mutating commands and fetches (for scripting)")
}
