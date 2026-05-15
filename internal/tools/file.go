package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func ReadFile(ctx context.Context, args map[string]any) (string, error) {
	return ReadFileFromRoot("")(ctx, args)
}

func ReadFileFromRoot(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
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

func WriteFile(ctx context.Context, args map[string]any) (string, error) {
	return WriteFileToRoot("")(ctx, args)
}

func WriteFileToRoot(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
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
