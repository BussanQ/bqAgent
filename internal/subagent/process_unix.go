//go:build !windows

package subagent

import (
	"os/exec"
	"syscall"
)

func configureWorkerProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func terminateWorkerPID(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// workerProcessAlive uses kill(pid, 0), which checks process existence without
// delivering a signal. EPERM still proves that the process exists.
func workerProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
