package tui

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// Mentioned files reuse the server's workspace attachment limits so a turn is
// never rejected because of an @ reference; extra or oversized files stay as
// plain @path text that the model can still read with tools.
const (
	maxMentionFiles      = 5
	maxMentionFileBytes  = 2 << 20
	maxMentionFilesBytes = 6 << 20
	maxIndexedFiles      = 50_000
	fileIndexTTL         = 10 * time.Second
	fileIndexTimeout     = 5 * time.Second
)

// mentionQuery is the @token around the cursor, in rune offsets of the
// displayed prompt.
type mentionQuery struct {
	start int
	end   int
	query string
}

type workspaceFileIndex struct {
	files    []string
	loadedAt time.Time
	loading  bool
	wanted   bool
}

type fileIndexMsg struct {
	files []string
}

var skippedIndexDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "__pycache__": true,
	".venv": true, ".idea": true, ".cache": true, ".next": true, ".gradle": true,
}

// indexWorkspaceFiles prefers git so .gitignore is respected and falls back to
// a bounded directory walk outside repositories.
func indexWorkspaceFiles(root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fileIndexTimeout)
		defer cancel()
		if files, err := gitWorkspaceFiles(ctx, root); err == nil {
			return fileIndexMsg{files: files}
		}
		return fileIndexMsg{files: walkWorkspaceFiles(ctx, root)}
	}
}

func gitWorkspaceFiles(ctx context.Context, root string) ([]string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []string
	for _, entry := range bytes.Split(output, []byte{0}) {
		name := string(entry)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		files = append(files, name)
		if len(files) >= maxIndexedFiles {
			break
		}
	}
	return files, nil
}

func walkWorkspaceFiles(ctx context.Context, root string) []string {
	var files []string
	filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || len(files) >= maxIndexedFiles {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if current != root && skippedIndexDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			if relative, err := filepath.Rel(root, current); err == nil {
				files = append(files, filepath.ToSlash(relative))
			}
		}
		return nil
	})
	return files
}

// searchFiles ranks basename matches before directory matches, then shallower
// and shorter paths, like the file pickers of other coding agents.
func searchFiles(files []string, query string, limit int) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	type match struct {
		path  string
		score int
		depth int
	}
	matches := make([]match, 0, min(len(files), 256))
	for _, file := range files {
		lower := strings.ToLower(file)
		base := path.Base(lower)
		score := -1
		switch {
		case query == "":
			score = 0
		case base == query:
			score = 0
		case strings.HasPrefix(base, query):
			score = 1
		case strings.Contains(base, query):
			score = 2
		case strings.HasPrefix(lower, query):
			score = 3
		case strings.Contains(lower, query):
			score = 4
		case isSubsequence(lower, query):
			score = 5
		}
		if score >= 0 {
			matches = append(matches, match{path: file, score: score, depth: strings.Count(file, "/")})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		left, right := matches[i], matches[j]
		if left.score != right.score {
			return left.score < right.score
		}
		if left.depth != right.depth {
			return left.depth < right.depth
		}
		if len(left.path) != len(right.path) {
			return len(left.path) < len(right.path)
		}
		return left.path < right.path
	})
	result := make([]string, 0, min(limit, len(matches)))
	for _, item := range matches[:min(limit, len(matches))] {
		result = append(result, item.path)
	}
	return result
}

func isSubsequence(value, query string) bool {
	remaining := []rune(query)
	for _, char := range value {
		if len(remaining) == 0 {
			break
		}
		if char == remaining[0] {
			remaining = remaining[1:]
		}
	}
	return len(remaining) == 0
}

// activeMention finds an @token that starts a word and contains the cursor.
func activeMention(value []rune, cursor int) (mentionQuery, bool) {
	cursor = min(max(0, cursor), len(value))
	start := cursor
	for start > 0 && !unicode.IsSpace(value[start-1]) {
		start--
	}
	if start >= len(value) || value[start] != '@' || start == cursor {
		return mentionQuery{}, false
	}
	end := cursor
	for end < len(value) && !unicode.IsSpace(value[end]) {
		end++
	}
	return mentionQuery{start: start, end: end, query: string(value[start+1 : cursor])}, true
}

func mentionText(file string) string {
	if strings.ContainsFunc(file, unicode.IsSpace) {
		return `@"` + file + `" `
	}
	return "@" + file + " "
}

var mentionPattern = regexp.MustCompile(`(?:^|\s)@(?:"([^"\n]+)"|([^\s"]+))`)

// mentionedFiles resolves @references to regular workspace files within the
// attachment limits. Trailing punctuation is ignored, so "@go.mod," works.
func mentionedFiles(workspace, message string) []string {
	if strings.TrimSpace(workspace) == "" {
		return nil
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil
	}
	defer root.Close()
	seen := map[string]bool{}
	var files []string
	total := int64(0)
	for _, match := range mentionPattern.FindAllStringSubmatch(message, -1) {
		if len(files) >= maxMentionFiles {
			break
		}
		candidate := match[1]
		if candidate == "" {
			candidate = match[2]
		}
		for candidate != "" {
			relative := filepath.ToSlash(filepath.Clean(filepath.FromSlash(candidate)))
			if !filepath.IsAbs(candidate) && filepath.IsLocal(filepath.FromSlash(relative)) {
				if info, err := root.Stat(filepath.FromSlash(relative)); err == nil && info.Mode().IsRegular() {
					if !seen[relative] && info.Size() <= maxMentionFileBytes && total+info.Size() <= maxMentionFilesBytes {
						seen[relative] = true
						total += info.Size()
						files = append(files, relative)
					}
					break
				}
			}
			trimmed := strings.TrimRight(candidate, ".,;:!?)]}>'，。；：！？）】」")
			if trimmed == candidate {
				break
			}
			candidate = trimmed
		}
	}
	return files
}
