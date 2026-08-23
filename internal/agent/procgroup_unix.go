//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group and wires Cancel to
// SIGKILL the whole group. exec.CommandContext alone kills only bash itself,
// leaving grandchildren (a hung test binary, a dev server) running orphaned.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // negative pid = whole group
	}
}
