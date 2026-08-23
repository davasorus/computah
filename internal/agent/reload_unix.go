//go:build !windows

package agent

import "syscall"

// execReplace replaces the current process image with a new one (execve). On
// success it does not return — the new binary takes over this PID, so /reload
// seamlessly resumes the session in the freshly built agent.
func execReplace(bin string, args, env []string) error {
	return syscall.Exec(bin, args, env)
}
