//go:build windows

package subagent

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureWorkerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
func terminateWorkerPID(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

// tasklist exits successfully only when the requested PID is currently listed.
// It is the Windows equivalent of the Unix kill(pid, 0) existence check.
func workerProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	output, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	return err == nil && strings.Contains(string(output), strconv.Itoa(pid))
}
