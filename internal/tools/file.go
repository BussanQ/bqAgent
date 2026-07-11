package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		offset, err := optionalPositiveInt(args, "offset")
		if err != nil {
			return "", err
		}
		limit, err := optionalPositiveInt(args, "limit")
		if err != nil {
			return "", err
		}

		resolvedPath, _, err := normalizeWorkspacePath(root, path)
		if err != nil {
			return "", err
		}
		content, err := os.ReadFile(resolvedPath)
		if err != nil {
			return "", fmt.Errorf("failed to read %q: %w", path, err)
		}
		if offset == 0 && limit == 0 {
			return string(content), nil
		}
		return sliceLines(string(content), offset, limit), nil
	}
}

// sliceLines returns the lines of content starting at the 1-based offset (0 means
// from the first line) for up to limit lines (0 means to the end).
func sliceLines(content string, offset, limit int) string {
	lines := strings.Split(content, "\n")
	start := 0
	if offset > 0 {
		start = offset - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "\n")
}

// optionalPositiveInt reads an optional string-encoded non-negative integer
// argument (sticking to the codebase's string-param convention). Missing/empty
// returns 0; a non-numeric or negative value is an error.
func optionalPositiveInt(args map[string]any, key string) (int, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, nil
	}
	var text string
	switch value := raw.(type) {
	case string:
		text = strings.TrimSpace(value)
	case float64:
		return int(value), nil
	default:
		return 0, fmt.Errorf("argument %q must be a string integer", key)
	}
	if text == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(text)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("argument %q must be a non-negative integer", key)
	}
	return parsed, nil
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

		resolvedPath, relativePath, err := normalizeWorkspacePath(root, path)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(resolvedPath, []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("failed to write %q: %w", path, err)
		}
		return fmt.Sprintf("Wrote to %s", relativePath), nil
	}
}

func EditFile(ctx context.Context, args map[string]any) (string, error) {
	return EditFileInRoot("")(ctx, args)
}

// EditFileInRoot performs an exact string replacement in a file. old_string must
// match exactly once unless replace_all is true. This mirrors Claude Code's Edit
// tool: it is far more token-efficient and safer than rewriting the whole file.
func EditFileInRoot(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		path, err := requireString(args, "path")
		if err != nil {
			return "", err
		}
		oldString, err := requireString(args, "old_string")
		if err != nil {
			return "", err
		}
		newString, err := requireString(args, "new_string")
		if err != nil {
			return "", err
		}
		if oldString == newString {
			return "", fmt.Errorf("old_string and new_string must be different")
		}
		replaceAll := parseBoolArg(args, "replace_all")

		resolvedPath, relativePath, err := normalizeWorkspacePath(root, path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(resolvedPath)
		if err != nil {
			return "", fmt.Errorf("failed to read %q: %w", path, err)
		}
		content := string(data)
		count := strings.Count(content, oldString)
		if count == 0 {
			return "", fmt.Errorf("old_string not found in %q", path)
		}
		if count > 1 && !replaceAll {
			return "", fmt.Errorf("old_string is not unique in %q (%d matches); add more context or set replace_all", path, count)
		}

		var updated string
		if replaceAll {
			updated = strings.ReplaceAll(content, oldString, newString)
		} else {
			updated = strings.Replace(content, oldString, newString, 1)
			count = 1
		}
		if err := os.WriteFile(resolvedPath, []byte(updated), 0o644); err != nil {
			return "", fmt.Errorf("failed to write %q: %w", path, err)
		}
		return fmt.Sprintf("Edited %s (%d replacement(s))", relativePath, count), nil
	}
}

// parseBoolArg reads an optional string/bool argument as a boolean (default
// false), following the codebase's string-param convention (e.g. install_skill's
// overwrite).
func parseBoolArg(args map[string]any, key string) bool {
	switch value := args[key].(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// normalizeWorkspacePath accepts workspace-relative paths and also tolerates an
// absolute path when it resolves inside root. Paths outside the workspace are
// rejected instead of being passed to the filesystem, which gives the model a
// fast, actionable error rather than letting a malformed absolute path loop.
func normalizeWorkspacePath(root, path string) (absolute string, relative string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", fmt.Errorf("path is required and must be workspace-relative")
	}
	if strings.TrimSpace(root) == "" {
		return path, path, nil
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)

	var candidate string
	if isAbsoluteLike(path) {
		candidate = filepath.Clean(filepath.FromSlash(path))
		if !filepath.IsAbs(candidate) {
			return "", "", fmt.Errorf("path %q is an absolute path for another platform; use a workspace-relative path such as the paths returned by glob", path)
		}
	} else {
		candidate = filepath.Join(rootAbs, filepath.FromSlash(path))
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("path %q is outside workspace %q; use a workspace-relative path such as the paths returned by glob", path, filepath.ToSlash(rootAbs))
	}
	if err := ensureWorkspaceSymlinkBoundary(rootAbs, candidate); err != nil {
		return "", "", fmt.Errorf("path %q is outside workspace through a symbolic link; use a workspace-relative path inside the workspace: %w", path, err)
	}
	return candidate, filepath.ToSlash(rel), nil
}

func ensureWorkspaceSymlinkBoundary(root, candidate string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	existing := candidate
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil
		}
		existing = parent
	}
	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedExisting)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("resolved path %q is not under %q", resolvedExisting, resolvedRoot)
	}
	return nil
}

func isAbsoluteLike(path string) bool {
	path = strings.TrimSpace(path)
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}
