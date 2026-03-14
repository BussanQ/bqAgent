package tools

import (
	"bytes"
	"errors"
	"os/exec"
	"runtime"
)

func ExecuteBash(args map[string]any) (string, error) {
	command, err := requireString(args, "command")
	if err != nil {
		return "", err
	}

	cmd := shellCommand(command)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return "", err
		}
	}

	return stdout.String() + stderr.String(), nil
}

func shellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-c", command)
}
