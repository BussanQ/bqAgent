package tools

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultGrepMaxResults = 200
	defaultGlobMaxResults = 1000
	grepMaxFileBytes      = 5 << 20 // skip files larger than 5 MiB
)

func Grep(ctx context.Context, args map[string]any) (string, error) {
	return GrepInRoot("")(ctx, args)
}

// GrepInRoot searches file contents for a Go regexp, returning path:line:text
// lines. It is pure Go (no external ripgrep) for consistent cross-platform
// behavior, skips .git and binary files, and caps results.
func GrepInRoot(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		pattern, err := requireString(args, "pattern")
		if err != nil {
			return "", err
		}
		searchPath, _ := optionalString(args, "path")
		globFilter, _ := optionalString(args, "glob")
		ignoreCase := parseBoolArg(args, "ignore_case")
		maxResults, err := optionalPositiveInt(args, "max_results")
		if err != nil {
			return "", err
		}
		if maxResults == 0 {
			maxResults = defaultGrepMaxResults
		}

		expr := pattern
		if ignoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}

		base := resolvePath(root, searchPath)
		if strings.TrimSpace(searchPath) == "" {
			base = resolvePath(root, ".")
		}

		var matches []string
		truncated := false
		walkErr := walkFiles(ctx, base, func(absPath string) error {
			if globFilter != "" {
				if ok, _ := filepath.Match(globFilter, filepath.Base(absPath)); !ok {
					return nil
				}
			}
			info, statErr := os.Stat(absPath)
			if statErr != nil || info.Size() > grepMaxFileBytes {
				return nil
			}
			data, readErr := os.ReadFile(absPath)
			if readErr != nil || isBinary(data) {
				return nil
			}
			rel := displayPath(root, absPath)
			for index, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, index+1, strings.TrimRight(line, "\r")))
					if len(matches) >= maxResults {
						truncated = true
						return errStopWalk
					}
				}
			}
			return nil
		})
		if walkErr != nil && walkErr != errStopWalk {
			return "", walkErr
		}
		if len(matches) == 0 {
			return "No matches found.", nil
		}
		result := strings.Join(matches, "\n")
		if truncated {
			result += fmt.Sprintf("\n... (truncated at %d matches)", maxResults)
		}
		return result, nil
	}
}

func Glob(ctx context.Context, args map[string]any) (string, error) {
	return GlobInRoot("")(ctx, args)
}

// GlobInRoot returns file paths matching a glob pattern (supports ** for any
// depth), most-recently-modified first, skipping .git and capping results.
func GlobInRoot(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		pattern, err := requireString(args, "pattern")
		if err != nil {
			return "", err
		}
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		searchPath, _ := optionalString(args, "path")
		base := resolvePath(root, searchPath)
		if strings.TrimSpace(searchPath) == "" {
			base = resolvePath(root, ".")
		}

		type entry struct {
			path    string
			modTime int64
		}
		var entries []entry
		walkErr := walkFiles(ctx, base, func(absPath string) error {
			rel, relErr := filepath.Rel(base, absPath)
			if relErr != nil {
				return nil
			}
			if matchDoubleStar(pattern, filepath.ToSlash(rel)) {
				info, statErr := os.Stat(absPath)
				modTime := int64(0)
				if statErr == nil {
					modTime = info.ModTime().UnixNano()
				}
				entries = append(entries, entry{path: displayPath(root, absPath), modTime: modTime})
			}
			return nil
		})
		if walkErr != nil && walkErr != errStopWalk {
			return "", walkErr
		}
		if len(entries) == 0 {
			return "No files matched.", nil
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].modTime > entries[j].modTime })

		truncated := false
		if len(entries) > defaultGlobMaxResults {
			entries = entries[:defaultGlobMaxResults]
			truncated = true
		}
		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = e.path
		}
		result := strings.Join(paths, "\n")
		if truncated {
			result += fmt.Sprintf("\n... (truncated at %d files)", defaultGlobMaxResults)
		}
		return result, nil
	}
}

var errStopWalk = fmt.Errorf("stop walk")

// walkFiles visits regular files under base, skipping .git directories. base may
// itself be a single file.
func walkFiles(ctx context.Context, base string, visit func(absPath string) error) error {
	info, err := os.Stat(base)
	if err != nil {
		return fmt.Errorf("failed to access %q: %w", base, err)
	}
	if !info.IsDir() {
		return visit(base)
	}
	return filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return visit(path)
	})
}

func isBinary(data []byte) bool {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// displayPath renders an absolute path relative to root (forward slashes) for
// readable, stable tool output; falls back to the absolute path.
func displayPath(root, absPath string) string {
	if strings.TrimSpace(root) == "" {
		return filepath.ToSlash(absPath)
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}

// matchDoubleStar matches a slash-separated glob pattern against a slash-separated
// path, where ** matches zero or more path segments and other segments use
// filepath.Match semantics.
func matchDoubleStar(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			// Collapse consecutive ** and match the rest against any suffix.
			rest := pattern[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := filepath.Match(pattern[0], name[0]); !ok {
			return false
		}
		pattern = pattern[1:]
		name = name[1:]
	}
	return len(name) == 0
}
