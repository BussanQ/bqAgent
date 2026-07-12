//go:build windows

package extagent

import (
	"os/exec"
	"strconv"
	"syscall"
)

func configureExternalProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// taskkill /T terminates the full external-agent process tree. This is
		// the Windows equivalent of the Unix process-group cancellation path.
		killer := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		if err := killer.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
