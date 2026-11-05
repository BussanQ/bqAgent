package tools

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	defaultGrepMaxResults = 200
	// grepMaxResultsLimit is a hard cap applied even when the caller asks for
	// more, so one grep call cannot return an unbounded number of lines.
	grepMaxResultsLimit   = 1000
	defaultGlobMaxResults = 1000
	grepMaxFileBytes      = 5 << 20 // skip files larger than 5 MiB
	// grepMaxScannedBytes bounds the cumulative file content read by one grep
	// call, so a huge workspace cannot turn a single search into gigabytes of I/O.
	grepMaxScannedBytes = 64 << 20
	// grepMaxLineBytes bounds the text echoed for one matching line; minified
	// files can otherwise put megabytes on a single line.
	grepMaxLineBytes = 2000
	// grepMaxOutputBytes bounds the total text returned by one grep call.
	grepMaxOutputBytes  = 1 << 20
	maxGlobAlternatives = 64
)

func Grep(ctx context.Context, args map[string]any) (string, error) {
	return GrepInRoot("")(ctx, args)
}

// GrepInRoot searches file contents for a Go regexp, returning path:line:text
// lines. It is pure Go (no external ripgrep) for consistent cross-platform
// behavior, skips .git and binary files, and caps results, scanned bytes and
// output size. With a workspace root, all file access goes through os.Root so
// symbolic links cannot expose content outside the workspace.
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
		maxResults = min(maxResults, grepMaxResultsLimit)

		expr := pattern
		if ignoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return "", fmt.Errorf("invalid pattern: %w", err)
		}

		source, err := openSearchSource(root, searchPath)
		if err != nil {
			return "", err
		}
		defer source.Close()

		var output strings.Builder
		matchCount := 0
		var scanned int64
		stopReason := ""
		walkErr := source.walk(ctx, func(name string, entry fs.DirEntry) error {
			if globFilter != "" {
				if ok, _ := path.Match(globFilter, path.Base(name)); !ok {
					return nil
				}
			}
			info, infoErr := entry.Info()
			if infoErr != nil || info.Size() > grepMaxFileBytes {
				return nil
			}
			if scanned+info.Size() > grepMaxScannedBytes {
				stopReason = fmt.Sprintf("search stopped after scanning %d MiB; narrow path or glob", grepMaxScannedBytes>>20)
				return errStopWalk
			}
			data, readErr := source.readFile(name, grepMaxFileBytes)
			if readErr != nil {
				return nil
			}
			scanned += int64(len(data))
			if isBinary(data) {
				return nil
			}
			display := source.display(name)
			for index, line := range strings.Split(string(data), "\n") {
				if !re.MatchString(line) {
					continue
				}
				text := truncateUTF8(strings.TrimRight(line, "\r"), grepMaxLineBytes)
				formatted := fmt.Sprintf("%s:%d:%s", display, index+1, text)
				if output.Len()+len(formatted)+1 > grepMaxOutputBytes {
					stopReason = fmt.Sprintf("output truncated at %d bytes", grepMaxOutputBytes)
					return errStopWalk
				}
				if output.Len() > 0 {
					output.WriteByte('\n')
				}
				output.WriteString(formatted)
				matchCount++
				if matchCount >= maxResults {
					stopReason = fmt.Sprintf("truncated at %d matches", maxResults)
					return errStopWalk
				}
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errStopWalk) {
			return "", walkErr
		}
		if matchCount == 0 {
			if stopReason != "" {
				return "No matches found (" + stopReason + ").", nil
			}
			return "No matches found.", nil
		}
		result := output.String()
		if stopReason != "" {
			result += "\n... (" + stopReason + ")"
		}
		return result, nil
	}
}

func Glob(ctx context.Context, args map[string]any) (string, error) {
	return GlobInRoot("")(ctx, args)
}

// GlobInRoot returns file paths matching a glob pattern (supports ** for any
// depth and brace alternatives such as *.{go,md}), most-recently-modified first,
// skipping .git and capping results. Only the newest defaultGlobMaxResults
// matches are retained while walking, so memory stays bounded regardless of
// workspace size.
func GlobInRoot(root string) Function {
	return func(ctx context.Context, args map[string]any) (string, error) {
		pattern, err := requireString(args, "pattern")
		if err != nil {
			return "", err
		}
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		searchPath, _ := optionalString(args, "path")
		if isAbsoluteLike(pattern) {
			if strings.TrimSpace(root) == "" {
				return "", fmt.Errorf("absolute glob patterns are not supported; pass the directory in path and use a relative pattern")
			}
			rootSlash := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(root)), "/")
			workspacePrefix := rootSlash + "/"
			if len(pattern) >= len(workspacePrefix) && strings.EqualFold(pattern[:len(workspacePrefix)], workspacePrefix) {
				pattern = pattern[len(workspacePrefix):]
			} else {
				return "", fmt.Errorf("glob pattern %q is outside the workspace or uses another platform; use a relative pattern such as **/*.go", pattern)
			}
		}
		source, err := openSearchSource(root, searchPath)
		if err != nil {
			return "", err
		}
		defer source.Close()

		newest := &globEntryHeap{}
		matched := 0
		walkErr := source.walk(ctx, func(name string, entry fs.DirEntry) error {
			if !matchDoubleStar(pattern, source.relativeToStart(name)) {
				return nil
			}
			matched++
			modTime := int64(0)
			if info, infoErr := entry.Info(); infoErr == nil {
				modTime = info.ModTime().UnixNano()
			}
			heap.Push(newest, globEntry{path: source.display(name), modTime: modTime})
			if newest.Len() > defaultGlobMaxResults {
				heap.Pop(newest)
			}
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, errStopWalk) {
			return "", walkErr
		}
		if matched == 0 {
			return "No files matched.", nil
		}
		entries := []globEntry(*newest)
		sort.Slice(entries, func(i, j int) bool { return globEntryNewer(entries[i], entries[j]) })
		paths := make([]string, len(entries))
		for i, e := range entries {
			paths[i] = e.path
		}
		result := strings.Join(paths, "\n")
		if matched > len(entries) {
			result += fmt.Sprintf("\n... (truncated at %d files)", defaultGlobMaxResults)
		}
		return result, nil
	}
}

type globEntry struct {
	path    string
	modTime int64
}

// globEntryNewer orders by modification time descending, then path ascending,
// so equal timestamps produce deterministic output.
func globEntryNewer(a, b globEntry) bool {
	if a.modTime != b.modTime {
		return a.modTime > b.modTime
	}
	return a.path < b.path
}

// globEntryHeap is a min-heap whose root is the entry that would be listed
// last, so popping it keeps the newest N matches.
type globEntryHeap []globEntry

func (h globEntryHeap) Len() int           { return len(h) }
func (h globEntryHeap) Less(i, j int) bool { return globEntryNewer(h[j], h[i]) }
func (h globEntryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *globEntryHeap) Push(x any)        { *h = append(*h, x.(globEntry)) }
func (h *globEntryHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

var errStopWalk = errors.New("stop walk")

// searchSource is the filesystem view used by grep and glob. With a workspace
// root it is backed by os.Root, which refuses any access (including via
// symbolic links) that would resolve outside the workspace.
type searchSource struct {
	fsys    fs.FS
	start   string
	display func(name string) string
	closer  io.Closer
}

func openSearchSource(root, searchPath string) (*searchSource, error) {
	if strings.TrimSpace(searchPath) == "" {
		searchPath = "."
	}
	base, relative, err := normalizeWorkspacePath(root, searchPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(root) != "" {
		rootFS, openErr := os.OpenRoot(root)
		if openErr != nil {
			return nil, fmt.Errorf("failed to access workspace %q: %w", root, openErr)
		}
		start := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
		if start == "" {
			start = "."
		}
		if _, statErr := fs.Stat(rootFS.FS(), start); statErr != nil {
			_ = rootFS.Close()
			return nil, fmt.Errorf("failed to access %q: %w", searchPath, rootPathError(searchPath, statErr))
		}
		return &searchSource{
			fsys:    rootFS.FS(),
			start:   start,
			display: func(name string) string { return name },
			closer:  rootFS,
		}, nil
	}

	// No workspace boundary: search the requested path directly.
	info, err := os.Stat(base)
	if err != nil {
		return nil, fmt.Errorf("failed to access %q: %w", base, err)
	}
	dir, start := base, "."
	if !info.IsDir() {
		dir, start = filepath.Dir(base), filepath.Base(base)
	}
	return &searchSource{
		fsys:  os.DirFS(dir),
		start: start,
		display: func(name string) string {
			return filepath.ToSlash(filepath.Join(dir, filepath.FromSlash(name)))
		},
	}, nil
}

func (source *searchSource) Close() {
	if source != nil && source.closer != nil {
		_ = source.closer.Close()
	}
}

// relativeToStart returns name relative to the search start ("." when the
// search targets a single file), matching filepath.Rel(base, file) semantics.
func (source *searchSource) relativeToStart(name string) string {
	if source.start == "." {
		return name
	}
	if name == source.start {
		return "."
	}
	return strings.TrimPrefix(name, source.start+"/")
}

// readFile reads at most maxBytes; larger files are rejected so a file that
// grew after its size was checked cannot exceed the per-file budget.
func (source *searchSource) readFile(name string, maxBytes int64) ([]byte, error) {
	file, err := source.fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %q exceeds %d bytes", name, maxBytes)
	}
	return data, nil
}

// walk visits regular files under the search start, skipping .git directories
// and symbolic links. Unreadable entries are skipped rather than aborting the
// whole search.
func (source *searchSource) walk(ctx context.Context, visit func(name string, entry fs.DirEntry) error) error {
	return fs.WalkDir(source.fsys, source.start, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry == nil {
				return fmt.Errorf("failed to access %q: %w", name, err)
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return visit(name, entry)
	})
}

func isBinary(data []byte) bool {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// truncateUTF8 shortens text to at most maxBytes without splitting a rune.
func truncateUTF8(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// matchDoubleStar matches a slash-separated glob pattern against a slash-separated
// path, where ** matches zero or more path segments, {a,b} expands alternatives,
// and other segments use filepath.Match semantics.
func matchDoubleStar(pattern, name string) bool {
	for _, alternative := range expandGlobAlternatives(pattern, maxGlobAlternatives) {
		if matchSegments(strings.Split(alternative, "/"), strings.Split(name, "/")) {
			return true
		}
	}
	return false
}

func expandGlobAlternatives(pattern string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	closeOffset := strings.IndexByte(pattern[open+1:], '}')
	if closeOffset < 0 {
		return []string{pattern}
	}
	closeIndex := open + 1 + closeOffset
	choices := strings.Split(pattern[open+1:closeIndex], ",")
	if len(choices) < 2 {
		return []string{pattern}
	}

	results := make([]string, 0, len(choices))
	for _, choice := range choices {
		expanded := pattern[:open] + choice + pattern[closeIndex+1:]
		for _, alternative := range expandGlobAlternatives(expanded, limit-len(results)) {
			results = append(results, alternative)
			if len(results) >= limit {
				return results
			}
		}
	}
	return results
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
