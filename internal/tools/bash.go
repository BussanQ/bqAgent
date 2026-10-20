package tools

import (
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
	return ExecuteBashInDirWithMaxOutput(root, DefaultBashOutputMaxBytes)
}

// ExecuteBashInDirWithMaxOutput runs commands in root while bounding the
// combined stdout/stderr retained for the tool result.
func ExecuteBashInDirWithMaxOutput(root string, maxOutputBytes int64) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		command, err := requireString(args, "command")
		if err != nil {
			return "", err
		}

		if manager := BashJobsFromContext(ctx); manager != nil {
			return manager.execute(ctx, root, command, maxOutputBytes)
		}

		// A shared writer preserves the observed stdout/stderr write order while
		// placing both streams under one retention budget. It always accepts the
		// whole write so the pipe keeps draining after truncation.
		outputBuffer := newBoundedOutput(maxOutputBytes, DefaultBashOutputMaxBytes)
		proc, err := startBashProcess(ctx, root, command, outputBuffer)
		if err != nil {
			return "", err
		}
		err = proc.wait()
		output := outputBuffer.String()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return output, ctxErr
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return output, fmt.Errorf("command exited with status %d", exitErr.ExitCode())
			}
			return "", fmt.Errorf("command execution failed: %w", err)
		}

		return output, nil
	}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}
