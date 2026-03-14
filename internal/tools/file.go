package tools

import (
	"fmt"
	"os"
)

func ReadFile(args map[string]any) (string, error) {
	path, err := requireString(args, "path")
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func WriteFile(args map[string]any) (string, error) {
	path, err := requireString(args, "path")
	if err != nil {
		return "", err
	}
	content, err := requireString(args, "content")
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote to %s", path), nil
}
