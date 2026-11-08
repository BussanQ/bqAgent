package extagent

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"bqagent/internal/transcript"
)

const maxEditBytes = 96_000
const maxEditLines = 2000

type editPreview struct {
	transcript.FileEdits
	bytes, lines int
}

func (p *editPreview) file(path, old, kind string) *transcript.EditFile {
	if path == "" || len(path) > 4096 || len(old) > 4096 || strings.ContainsRune(path, 0) || strings.ContainsRune(old, 0) || len(p.Files) >= 32 {
		p.Partial = true
		return nil
	}
	p.Files = append(p.Files, transcript.EditFile{Path: strings.Clone(path), OldPath: strings.Clone(old), Kind: kind})
	return &p.Files[len(p.Files)-1]
}

func (p *editPreview) line(f *transcript.EditFile, kind, text string, old, new int) bool {
	left := maxEditBytes - p.bytes
	if left <= 0 || p.lines >= maxEditLines {
		f.Partial = true
		return false
	}
	if len(text) > left {
		for left > 0 && !utf8.RuneStart(text[left]) {
			left--
		}
		text = text[:left]
		f.Partial = true
	}
	f.Lines = append(f.Lines, transcript.EditLine{Kind: kind, Text: strings.Clone(text), Old: old, New: new})
	if kind == "add" {
		f.Added++
	} else if kind == "del" {
		f.Removed++
	}
	p.bytes += len(text) + 1
	p.lines++
	return true
}

func (p *editPreview) gap(f *transcript.EditFile) {
	if len(f.Lines) > 0 && f.Lines[len(f.Lines)-1].Kind != "gap" {
		p.line(f, "gap", "", 0, 0)
	}
}

// Bound the input before splitting or diffing, and never turn a clipped suffix
// into a complete line or strip indentation from source text.
func editLines(text string) ([]string, bool) {
	clipped := len(text) > maxEditBytes
	if clipped {
		end := maxEditBytes
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
	}
	if text == "" {
		return nil, clipped
	}
	lines := strings.SplitN(strings.ReplaceAll(text, "\r\n", "\n"), "\n", maxEditLines+1)
	if len(lines) > maxEditLines && lines[maxEditLines] != "" {
		return lines[:maxEditLines], true
	}
	if lines[len(lines)-1] == "" && strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines, clipped
}

func (p *editPreview) whole(f *transcript.EditFile, text string) {
	lines, clipped := editLines(text)
	f.Partial = clipped
	for i, s := range lines {
		var ok bool
		switch f.Kind {
		case "added":
			ok = p.line(f, "add", s, 0, i+1)
		case "deleted":
			ok = p.line(f, "del", s, i+1, 0)
		default:
			ok = p.line(f, "content", s, 0, i+1)
		}
		if !ok {
			break
		}
	}
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(,\d+)? \+(\d+)(,\d+)? @@`)

func (p *editPreview) unified(f *transcript.EditFile, text string) {
	lines, clipped := editLines(text)
	f.Partial = f.Partial || clipped
	old, new := 0, 0
	located := false
	for _, s := range lines {
		if pos := hunkHeader.FindStringSubmatch(s); pos != nil {
			p.gap(f)
			old, _ = strconv.Atoi(pos[1])
			new, _ = strconv.Atoi(pos[3])
			located = true
			continue
		}
		if strings.HasPrefix(s, "@@") {
			p.gap(f)
			located = false
			old, new = 0, 0
			continue
		}
		if !located && (strings.HasPrefix(s, "--- ") || strings.HasPrefix(s, "+++ ")) {
			continue
		}
		if len(s) == 0 {
			continue
		}
		kind := ""
		left, right := old, new
		switch s[0] {
		case '-':
			kind = "del"
			right = 0
			if located {
				old++
			}
		case '+':
			kind = "add"
			left = 0
			if located {
				new++
			}
		case ' ':
			kind = "context"
			if located {
				old++
				new++
			}
		default:
			continue
		}
		if !p.line(f, kind, s[1:], left, right) {
			break
		}
	}
}

// Prefer the provider's resolved hunks. For before/after pairs use a bounded
// line LCS and avoid assigning file positions to snippet edits.
func (p *editPreview) pair(f *transcript.EditFile, before, after string, positioned bool) {
	if p.lines >= maxEditLines || p.bytes >= maxEditBytes {
		f.Partial = true
		return
	}
	a, ca := editLines(before)
	b, cb := editLines(after)
	f.Partial = f.Partial || ca || cb
	f.Snippet = !positioned
	i, j := 0, 0
	row := func(kind, s string, old, new int) bool {
		if !positioned {
			old, new = 0, 0
		}
		return p.line(f, kind, s, old, new)
	}
	for i < len(a) && j < len(b) && a[i] == b[j] {
		i++
		j++
	}
	endA, endB := len(a), len(b)
	for endA > i && endB > j && a[endA-1] == b[endB-1] {
		endA--
		endB--
	}
	if i == endA && j == endB {
		return
	}
	p.gap(f)
	for x := max(0, i-3); x < i; x++ {
		if !row("context", a[x], x+1, x+1) {
			return
		}
	}
	startA, startB := i, j
	width := endB - startB + 1
	var lcs []uint16
	if cells := (endA - i + 1) * width; cells <= 200_000 {
		lcs = make([]uint16, cells)
		for x := endA - 1; x >= i; x-- {
			for y := endB - 1; y >= j; y-- {
				k := (x-startA)*width + y - startB
				if a[x] == b[y] {
					lcs[k] = 1 + lcs[k+width+1]
				} else {
					lcs[k] = max(lcs[k+width], lcs[k+1])
				}
			}
		}
	} else {
		f.Partial = true
	}
	for i < endA || j < endB {
		k := (i-startA)*width + j - startB
		switch {
		case i < endA && j < endB && a[i] == b[j]:
			count := 0
			for i+count < endA && j+count < endB && a[i+count] == b[j+count] {
				count++
			}
			for k := 0; k < count; k++ {
				if count > 6 && k == 3 {
					p.gap(f)
					k = count - 3
				}
				if !row("context", a[i+k], i+k+1, j+k+1) {
					return
				}
			}
			i += count
			j += count
		case i < endA && (j == endB || lcs == nil || lcs[k+width] >= lcs[k+1]):
			if !row("del", a[i], i+1, 0) {
				return
			}
			i++
		default:
			if !row("add", b[j], 0, j+1) {
				return
			}
			j++
		}
	}
	for n := 0; n < 3 && i < len(a) && j < len(b); n++ {
		if !row("context", a[i], i+1, j+1) {
			return
		}
		i++
		j++
	}
}
