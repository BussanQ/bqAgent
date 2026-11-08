package extagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

type cliStderr struct{ buffer bytes.Buffer }

func (b *cliStderr) Write(p []byte) (int, error) {
	n := len(p)
	if room := 8192 - b.buffer.Len(); room > 0 {
		b.buffer.Write(p[:min(room, n)])
	}
	return n, nil
}

// Drain stdout while the agent is working. If the decoder stops early, reap
// the child rather than waiting on a writer blocked on its unread stdout.
func runCLIStream(ctx context.Context, cmd *exec.Cmd, read func(io.Reader) error) error {
	var stderr cliStderr
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	readErr := read(stdout)
	if readErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) && stderr.buffer.Len() > 0 {
		exit.Stderr = stderr.buffer.Bytes()
		return formatCLIError(waitErr)
	}
	if readErr != nil {
		return readErr
	}
	return waitErr
}
