package cmd

import (
	"fmt"
	"os"
)

// exitError converts a nonzero agent exit code into an error that Execute
// turns into the matching process exit code (without a usage dump).
func exitError(code int) error {
	return &agentExit{code: code}
}

type agentExit struct{ code int }

func (e *agentExit) Error() string { return fmt.Sprintf("exited with code %d", e.code) }

// handleExit lets Execute translate an agentExit into os.Exit(code). Called
// from Execute's error path.
func handleExit(err error) bool {
	if ae, ok := err.(*agentExit); ok {
		os.Exit(ae.code)
		return true
	}
	return false
}
