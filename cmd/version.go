package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags "-X .../cmd.version=X.Y.Z".
// When unset (e.g. go run / go install without ldflags), it falls back to the
// module version embedded by the Go toolchain.
var version = ""

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the computah version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("computah", resolveVersion())
	},
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}
