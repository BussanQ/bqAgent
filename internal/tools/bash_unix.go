//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

func prepareBashProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	configureProcessGroup(cmd)
	return func() error { return nil }, func() error { killProcessGroup(cmd); return nil }, nil
}

// configureProcessGroup puts the command in its own session and process group and, on
// context cancellation, kills the entire group rather than just the top-level
// shell. Without this a shell that spawns a long-lived/daemonized grandchild
// (e.g. `agently-cli auth login`) leaves the grandchild running and holding the
// inherited stdout/stderr pipe after the shell is killed, which keeps cmd.Run
// blocked until commandWaitDelay expires and leaks the process.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative PID targets the whole process group created via Setsid (pgid == pid).
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// killProcessGroup kills anything still alive in the command's process group.
// It is called after the shell exits so daemonized children do not leak.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
