package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

func ReadFile(args map[string]any) (string, error) {
	return ReadFileFromRoot("")(args)
}

func ReadFileFromRoot(root string) Function {
	return func(args map[string]any) (string, error) {
		path, err := requireString(args, "path")
		if err != nil {
			return "", err
		}

		content, err := os.ReadFile(resolvePath(root, path))
		if err != nil {
			return "", fmt.Errorf("failed to read %q: %w", path, err)
		}
		return string(content), nil
	}
}

func WriteFile(args map[string]any) (string, error) {
	return WriteFileToRoot("")(args)
}

func WriteFileToRoot(root string) Function {
	return func(args map[string]any) (string, error) {
		path, err := requireString(args, "path")
		if err != nil {
			return "", err
		}
		content, err := requireString(args, "content")
		if err != nil {
			return "", err
		}

		resolvedPath := resolvePath(root, path)
		if err := os.WriteFile(resolvedPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("failed to write %q: %w", path, err)
		}
		return fmt.Sprintf("Wrote to %s", resolvedPath), nil
	}
}

func resolvePath(root, path string) string {
	if root == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}
