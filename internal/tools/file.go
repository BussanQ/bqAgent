package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bqagent/internal/atomicfile"
	"bqagent/internal/safepath"
)

func ReadFile(ctx context.Context, args map[string]any) (string, error) {
	return ReadFileFromRoot("")(ctx, args)
}

func ReadFileFromRoot(root string) Function {
	return ReadFileFromRootWithMaxBytes(root, DefaultReadFileMaxBytes)
}

// ReadFileFromRootWithMaxBytes reads selected lines without retaining skipped
// content, oversized lines, or returned content beyond maxBytes.
func ReadFileFromRootWithMaxBytes(root string, maxBytes int64) Function {
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
		if err := ctx.Err(); err != nil {
			return "", err
		}

		resolvedPath, relativePath, err := normalizeWorkspacePath(root, path)
		if err != nil {
			return "", err
		}
		var file *os.File
		var rootFS *os.Root
		if strings.TrimSpace(root) == "" {
			file, err = os.Open(resolvedPath)
		} else {
			rootFS, err = os.OpenRoot(root)
			if err == nil {
				file, err = rootFS.Open(relativePath)
			}
		}
		if err != nil {
			if rootFS != nil {
				_ = rootFS.Close()
			}
			return "", fmt.Errorf("failed to read %q: %w", path, rootPathError(path, err))
		}
		defer file.Close()
		if rootFS != nil {
			defer rootFS.Close()
		}

		output := newBoundedOutput(maxBytes, DefaultReadFileMaxBytes)
		reader := bufio.NewReader(file)
		startLine := offset
		if startLine == 0 {
			startLine = 1
		}
		line := 1
		selected := 0

		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			fragment, readErr := reader.ReadSlice('\n')
			include := line >= startLine && (limit == 0 || selected < limit)
			if include && len(fragment) > 0 {
				// The old strings.Split/Join implementation excludes the newline
				// after the final limited line, so avoid counting that delimiter
				// against the output budget in the first place.
				if limit > 0 && selected+1 == limit && readErr == nil {
					fragment = fragment[:len(fragment)-1]
				}
				_, _ = output.Write(fragment)
			}

			switch {
			case readErr == nil:
				if include {
					selected++
				}
				line++
				if limit > 0 && selected >= limit {
					return output.String(), nil
				}
			case errors.Is(readErr, bufio.ErrBufferFull):
				// Continue draining this line in bounded fragments. The current
				// line number and selection state remain unchanged until its '\n'.
			case errors.Is(readErr, io.EOF):
				return output.String(), nil
			default:
				return "", fmt.Errorf("failed to read %q: %w", path, readErr)
			}
		}
	}
}

// optionalPositiveInt reads an optional non-negative integer argument. It
// accepts strings and float64 values decoded from JSON. Missing or empty values
// return 0; non-integral or negative values are errors.
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
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || math.Trunc(value) != value {
			return 0, fmt.Errorf("argument %q must be a non-negative integer", key)
		}
		parsed := int(value)
		if parsed < 0 || float64(parsed) != value {
			return 0, fmt.Errorf("argument %q must be a non-negative integer", key)
		}
		return parsed, nil
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
		if strings.TrimSpace(root) == "" {
			err = os.WriteFile(resolvedPath, []byte(content), 0o644)
		} else {
			rootFS, openErr := os.OpenRoot(root)
			if openErr != nil {
				err = openErr
			} else {
				err = atomicfile.WriteRoot(rootFS, relativePath, []byte(content), 0o644)
				_ = rootFS.Close()
			}
		}
		if err != nil {
			return "", fmt.Errorf("failed to write %q: %w", path, rootPathError(path, err))
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
		var data []byte
		var rootFS *os.Root
		if strings.TrimSpace(root) == "" {
			data, err = os.ReadFile(resolvedPath)
		} else {
			rootFS, err = os.OpenRoot(root)
			if err == nil {
				data, err = rootFS.ReadFile(relativePath)
			}
		}
		if err != nil {
			if rootFS != nil {
				_ = rootFS.Close()
			}
			return "", fmt.Errorf("failed to read %q: %w", path, rootPathError(path, err))
		}
		if rootFS != nil {
			defer func() {
				if rootFS != nil {
					_ = rootFS.Close()
				}
			}()
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
		if rootFS != nil {
			err = atomicfile.WriteRoot(rootFS, relativePath, []byte(updated), 0o644)
			closeErr := rootFS.Close()
			rootFS = nil
			if err == nil {
				err = closeErr
			}
		} else {
			err = os.WriteFile(resolvedPath, []byte(updated), 0o644)
		}
		if err != nil {
			return "", fmt.Errorf("failed to write %q: %w", path, rootPathError(path, err))
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
	logicalCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	logicalCandidate = filepath.Clean(logicalCandidate)
	rootAbs, rel, err := safepath.Relative(rootAbs, logicalCandidate)
	if err != nil {
		return "", "", fmt.Errorf("path %q is outside workspace %q; use a workspace-relative path such as the paths returned by glob: %w", path, filepath.ToSlash(rootAbs), err)
	}
	return filepath.Join(rootAbs, rel), filepath.ToSlash(rel), nil
}

func rootPathError(path string, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "escapes") {
		return fmt.Errorf("path %q is outside workspace through a symbolic link; use a workspace-relative path inside the workspace: %w", path, err)
	}
	return err
}

func isAbsoluteLike(path string) bool {
	path = strings.TrimSpace(path)
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}
