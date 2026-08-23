//go:build windows

package agent

import (
	"os"
	"os/exec"
)

// execReplace has no execve equivalent on Windows, so it spawns the new process
// inheriting stdio and exits, approximating the hand-off.
func execReplace(bin string, args, env []string) error {
	cmd := exec.Command(bin, args[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
