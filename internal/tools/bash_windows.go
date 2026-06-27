//go:build windows

package tools

import "os/exec"

// configureProcessGroup is a no-op on Windows. The default CommandContext
// cancellation (Process.Kill) together with cmd.WaitDelay handles teardown;
// killing a full process subtree on Windows would require a Job object, which is
// out of scope here.
func configureProcessGroup(cmd *exec.Cmd) {}
