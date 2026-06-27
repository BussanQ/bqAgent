package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// commandWaitDelay bounds how long cmd.Run blocks copying I/O after the process
// has exited or the context has been canceled. Without it, a command that spawns
// a long-lived/daemonized grandchild which inherits and keeps the stdout/stderr
// pipe open (e.g. an interactive `auth login`) wedges cmd.Run forever — even
// after the context deadline fires. In server/channel mode that froze a turn and,
// via the per-session lock, the whole conversation. The delay lets the kill take
// effect, then forcibly closes the pipes so Run always returns.
const commandWaitDelay = 5 * time.Second

func ExecuteBash(ctx context.Context, args map[string]any) (string, error) {
	return ExecuteBashInDir("")(ctx, args)
}

func ExecuteBashInDir(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		command, err := requireString(args, "command")
		if err != nil {
			return "", err
		}

		cmd := shellCommand(ctx, command)
		if root != "" {
			cmd.Dir = root
		}
		// A nil Stdin is wired to the null device, so commands that read stdin
		// get EOF instead of blocking on an interactive prompt.
		cmd.Stdin = nil
		// Bound the post-cancel I/O wait and kill the whole process tree on
		// cancellation (Unix); see configureProcessGroup and commandWaitDelay.
		cmd.WaitDelay = commandWaitDelay
		configureProcessGroup(cmd)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return "", fmt.Errorf("command execution failed: %w", err)
			}
		}

		return stdout.String() + stderr.String(), nil
	}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
