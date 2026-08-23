//go:build windows

package agent

import "os/exec"

// setProcessGroup is a no-op on Windows, which has no POSIX process groups.
// exec.CommandContext's default Cancel (Process.Kill) still terminates the
// direct child on timeout; killing a full process tree would require Job
// Objects, which this local agent doesn't need on its primary Linux/WSL2 target.
func setProcessGroup(cmd *exec.Cmd) {}
