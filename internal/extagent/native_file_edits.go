package extagent

import (
	"encoding/json"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"

	"bqagent/internal/transcript"
)

func codexFileEdits(raw json.RawMessage) *transcript.FileEdits {
	var changes []struct {
		Path     string
		Kind     json.RawMessage
		Diff     *string
		MovePath string
	}
	if json.Unmarshal(raw, &changes) != nil || changes == nil {
		return nil
	}
	p := editPreview{}
	for _, change := range changes {
		var kind struct {
			Type, MovePath string
			Move           string `json:"move_path"`
		}
		if json.Unmarshal(change.Kind, &kind) != nil {
			_ = json.Unmarshal(change.Kind, &kind.Type)
		}
		move := firstNonEmpty(kind.MovePath, kind.Move, change.MovePath)
		if change.Diff != nil && kind.Type != "add" && kind.Type != "delete" {
			if index := strings.LastIndex(*change.Diff, "\nMoved to: "); index >= 0 {
				body, tail := (*change.Diff)[:index], strings.TrimSpace((*change.Diff)[index+len("\nMoved to: "):])
				if tail != "" && !strings.Contains(tail, "\n") {
					move = firstNonEmpty(move, strings.TrimSpace(tail))
					change.Diff = &body
				}
			}
		}
		if change.Diff == nil && move == "" {
			continue
		}
		path, old, action := change.Path, "", "edited"
		switch kind.Type {
		case "add":
			action = "added"
		case "delete":
			action = "deleted"
		}
		if move != "" {
			path, old, action = move, change.Path, "renamed"
		}
		f := p.file(path, old, action)
		if f == nil {
			continue
		}
		if change.Diff != nil {
			if action == "added" || action == "deleted" {
				p.whole(f, *change.Diff)
			} else {
				p.unified(f, *change.Diff)
				if f.Kind == "edited" && f.Added+f.Removed == 0 {
					f.Partial = true
				}
			}
		}
	}
	return &p.FileEdits
}

func inputText(input map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if s, ok := input[key].(string); ok {
			return s, true
		}
	}
	return "", false
}

func editInput(name string, input map[string]any) *transcript.FileEdits {
	if name == "apply_patch" {
		raw, _ := inputText(input, "patch", "input", "value", "arguments", "command")
		return beginPatchEdits(raw)
	}
	if name == "commandExecution" || name == "bash" || name == "Bash" {
		command, _ := inputText(input, "command")
		return shellPatchEdits(command)
	}
	path, _ := inputText(input, "file_path", "path")
	if path == "" {
		return nil
	}
	p := editPreview{}
	switch name {
	case "Write", "write":
		content, ok := inputText(input, "content", "file_text")
		if !ok {
			return nil
		}
		if f := p.file(path, "", "written"); f != nil {
			p.whole(f, content)
		}
	case "Edit", "MultiEdit", "edit", "str_replace":
		f := p.file(path, "", "edited")
		if f == nil {
			return nil
		}
		pair := func(v map[string]any) bool {
			old, ok := inputText(v, "old_string", "oldString", "oldText", "old_str")
			next, has := inputText(v, "new_string", "newString", "newText", "new_str")
			if !ok || !has {
				return false
			}
			p.pair(f, old, next, false)
			return true
		}
		if edits, ok := input["edits"].([]any); ok {
			for _, e := range edits {
				v, _ := e.(map[string]any)
				if !pair(v) {
					f.Partial = true
				}
				if p.lines >= maxEditLines || p.bytes >= maxEditBytes {
					f.Partial = true
					break
				}
			}
		} else if !pair(input) {
			return nil
		}
		if f.Added+f.Removed == 0 && !f.Partial {
			return nil
		}
	default:
		return nil
	}
	return &p.FileEdits
}

func acpFileEdits(raw json.RawMessage) *transcript.FileEdits {
	var blocks []struct {
		Type, Path       string
		OldText, NewText *string
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	p := editPreview{}
	for _, b := range blocks {
		if b.Type != "diff" || b.NewText == nil {
			continue
		}
		kind := "edited"
		if b.OldText == nil {
			kind = "added"
		}
		f := p.file(b.Path, "", kind)
		if f == nil {
			continue
		}
		if b.OldText == nil {
			p.whole(f, *b.NewText)
		} else {
			p.pair(f, *b.OldText, *b.NewText, true)
			if f.Added+f.Removed == 0 && !f.Partial {
				p.Files = p.Files[:len(p.Files)-1]
			}
		}
	}
	if blocks == nil {
		return nil
	}
	return &p.FileEdits
}

var patchHeredoc = regexp.MustCompile(`^apply_?patch[ \t]+<<[ \t]*('[[:alnum:]_]+'|"[[:alnum:]_]+"|[[:alnum:]_]+)$`)

func shellPatchEdits(command string) *transcript.FileEdits {
	// Only accept a standalone invocation. A quoted patch, dry run, failed
	// command followed by "true", or conditional branch has no such proof.
	head, body, ok := strings.Cut(strings.TrimSpace(command), "\n")
	if !ok {
		return nil
	}
	match := patchHeredoc.FindStringSubmatch(strings.TrimSpace(head))
	if match == nil {
		return nil
	}
	delimiter := strings.Trim(match[1], `'"`)
	raw, ok := strings.CutSuffix(body, "\n"+delimiter)
	if !ok || strings.Contains(raw, "\n"+delimiter+"\n") {
		return nil
	}
	if delimiter == match[1] && strings.ContainsAny(raw, "$`\\") {
		return nil
	}
	return beginPatchEdits(raw)
}

// Callers have identified either the patch tool or an actual apply_patch
// invocation. Arbitrary command output or Write content can quote the format.
func beginPatchEdits(raw string) *transcript.FileEdits {
	lines, partial := editLines(raw)
	if len(lines) < 2 || lines[0] != "*** Begin Patch" || !partial && lines[len(lines)-1] != "*** End Patch" {
		return nil
	}
	p := editPreview{}
	var file *transcript.EditFile
	newLine := 0
	for _, line := range lines[1:] {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			file = p.file(strings.TrimPrefix(line, "*** Add File: "), "", "added")
			newLine = 0
		case strings.HasPrefix(line, "*** Delete File: "):
			file = p.file(strings.TrimPrefix(line, "*** Delete File: "), "", "deleted")
			if file != nil {
				file.Partial = true
			}
		case strings.HasPrefix(line, "*** Update File: "):
			file = p.file(strings.TrimPrefix(line, "*** Update File: "), "", "edited")
			if file != nil {
				file.Snippet = true
			}
		case file == nil:
			continue
		case strings.HasPrefix(line, "*** Move to: "):
			dest := strings.TrimPrefix(line, "*** Move to: ")
			if dest != "" && len(dest) <= 4096 {
				file.OldPath, file.Path, file.Kind = file.Path, strings.Clone(dest), "renamed"
			}
		case strings.HasPrefix(line, "@@"):
			p.gap(file)
		case strings.HasPrefix(line, "+"):
			num := 0
			if file.Kind == "added" {
				newLine++
				num = newLine
			}
			p.line(file, "add", line[1:], 0, num)
		case strings.HasPrefix(line, "-"):
			p.line(file, "del", line[1:], 0, 0)
		case strings.HasPrefix(line, " "):
			p.line(file, "context", line[1:], 0, 0)
		}
	}
	p.Partial = p.Partial || partial
	if len(p.Files) == 0 {
		return nil
	}
	return &p.FileEdits
}

var piDiffRow = regexp.MustCompile(`^([-+ ]) *(\d+) (.*)$`)

func piFileEdits(path string, raw json.RawMessage) *transcript.FileEdits {
	var result struct{ Details struct{ Diff string } }
	if path == "" || json.Unmarshal(raw, &result) != nil || result.Details.Diff == "" {
		return nil
	}
	p := editPreview{}
	f := p.file(path, "", "edited")
	if f == nil {
		return nil
	}
	lines, clipped := editLines(result.Details.Diff)
	f.Partial = clipped
	for _, s := range lines {
		if strings.TrimSpace(s) == "..." {
			p.gap(f)
			continue
		}
		m := piDiffRow.FindStringSubmatch(s)
		if m == nil {
			return nil
		}
		n, err := strconv.Atoi(m[2])
		if err != nil || n < 1 {
			return nil
		}
		var ok bool
		switch m[1] {
		case "+":
			ok = p.line(f, "add", m[3], 0, n)
		case "-":
			ok = p.line(f, "del", m[3], n, 0)
		default:
			ok = p.line(f, "context", m[3], n, 0) // Pi numbers unchanged rows on the old side.
		}
		if !ok {
			break
		}
	}
	if f.Added+f.Removed == 0 {
		return nil
	}
	return &p.FileEdits
}

func claudeFileEdits(path string, raw json.RawMessage) *transcript.FileEdits {
	var result struct {
		Type, FilePath  string
		Content         *string
		StructuredPatch []struct {
			OldStart, NewStart int
			Lines              []string
		}
	}
	if path == "" || json.Unmarshal(raw, &result) != nil {
		return nil
	}
	if result.FilePath != "" && result.FilePath != path {
		// Claude can resolve a relative tool argument to an absolute path.
		relative := pathpkg.Clean(strings.ReplaceAll(path, "\\", "/"))
		resolved := pathpkg.Clean(strings.ReplaceAll(result.FilePath, "\\", "/"))
		absolute := pathpkg.IsAbs(resolved) || len(resolved) > 2 && resolved[1] == ':' && resolved[2] == '/'
		if relative != resolved && (!absolute || pathpkg.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || !strings.HasSuffix(resolved, "/"+relative)) {
			return nil
		}
		path = result.FilePath
	}
	p := editPreview{}
	kind := "edited"
	if result.Type == "create" {
		kind = "added"
	}
	f := p.file(path, "", kind)
	if f == nil {
		return nil
	}
	if len(result.StructuredPatch) == 0 {
		if kind != "added" || result.Content == nil {
			return nil
		}
		p.whole(f, *result.Content)
		return &p.FileEdits
	}
	for _, h := range result.StructuredPatch {
		p.gap(f)
		old, new := h.OldStart, h.NewStart
		for _, s := range h.Lines {
			if s == "" {
				continue
			}
			var ok bool
			switch s[0] {
			case ' ':
				ok = p.line(f, "context", s[1:], old, new)
				old++
				new++
			case '-':
				ok = p.line(f, "del", s[1:], old, 0)
				old++
			case '+':
				ok = p.line(f, "add", s[1:], 0, new)
				new++
			default:
				continue
			}
			if !ok {
				return &p.FileEdits
			}
		}
	}
	return &p.FileEdits
}
