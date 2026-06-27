//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the command in its own process group and, on
// context cancellation, kills the entire group rather than just the top-level
// shell. Without this a shell that spawns a long-lived/daemonized grandchild
// (e.g. `agently-cli auth login`) leaves the grandchild running and holding the
// inherited stdout/stderr pipe after the shell is killed, which keeps cmd.Run
// blocked until commandWaitDelay expires and leaks the process.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative PID targets the whole process group created via Setpgid.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
