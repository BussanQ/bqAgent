package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func writeWorkspaceFiles(t *testing.T, root string, files map[string]int) {
	t.Helper()
	for name, size := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSearchFilesRanksBasenameMatchesBeforePathMatches(t *testing.T) {
	files := []string{"internal/tui/run.go", "cmd/agent/main.go", "go.sum", "docs/golang/notes.md", "cmd/agent/doctor.go", "go.mod", "README.md", "internal/tui/goal.txt"}
	got := searchFiles(files, "go", 6)
	want := []string{"go.mod", "go.sum", "internal/tui/goal.txt", "cmd/agent/main.go", "cmd/agent/doctor.go", "internal/tui/run.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("search go = %q, want %q", got, want)
	}
	if got := searchFiles(files, "agmain", 5); len(got) != 1 || got[0] != "cmd/agent/main.go" {
		t.Fatalf("subsequence search = %q", got)
	}
	if got := searchFiles(files, "", 2); strings.Join(got, "|") != "go.mod|go.sum" {
		t.Fatalf("empty query = %q", got)
	}
	if got := searchFiles(files, "zzz", 5); len(got) != 0 {
		t.Fatalf("no match = %q", got)
	}
}

func TestActiveMentionRequiresAnAtTokenAtTheCursor(t *testing.T) {
	cases := []struct {
		value  string
		cursor int
		ok     bool
		query  string
		end    int
	}{
		{value: "看 @go", cursor: 5, ok: true, query: "go", end: 5},
		{value: "@", cursor: 1, ok: true, query: "", end: 1},
		{value: "@internal/tui rest", cursor: 3, ok: true, query: "in", end: 13},
		{value: "mail a@b.com", cursor: 12},
		{value: "@go done", cursor: 8},
		{value: "@go", cursor: 0},
	}
	for _, test := range cases {
		mention, ok := activeMention([]rune(test.value), test.cursor)
		if ok != test.ok || (ok && (mention.query != test.query || mention.end != test.end)) {
			t.Errorf("activeMention(%q, %d) = %#v %v", test.value, test.cursor, mention, ok)
		}
	}
}

func TestMentionedFilesResolveWorkspaceFilesWithinLimits(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFiles(t, root, map[string]int{
		"a.go": 1, "my doc.md": 1, "dir/b.go": 1, "c.txt": 1, "d.txt": 1, "e.txt": 1,
		"big.bin": maxMentionFileBytes + 1,
	})
	got := mentionedFiles(root, `看 @a.go, @"my doc.md" @missing.go @../a.go @dir @big.bin mail@c.txt @a.go @dir/b.go。`)
	if strings.Join(got, "|") != "a.go|my doc.md|dir/b.go" {
		t.Fatalf("mentioned files = %q", got)
	}
	got = mentionedFiles(root, "@a.go @dir/b.go @c.txt @d.txt @e.txt @my doc.md")
	if len(got) != maxMentionFiles {
		t.Fatalf("mention limit = %q", got)
	}
	if got := mentionedFiles("", "@a.go"); got != nil {
		t.Fatalf("no workspace = %q", got)
	}
}

func TestWalkWorkspaceFilesSkipsDependencyAndVCSDirectories(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFiles(t, root, map[string]int{"main.go": 1, "web/app.ts": 1, "node_modules/x/index.js": 1, ".git/config": 1})
	got := walkWorkspaceFiles(context.Background(), root)
	if strings.Join(got, "|") != "main.go|web/app.ts" {
		t.Fatalf("indexed files = %q", got)
	}
	if mentionText("my doc.md") != `@"my doc.md" ` || mentionText("go.mod") != "@go.mod " {
		t.Fatal("mention text must quote paths with spaces")
	}
}

func typeText(model Model, text string) (Model, tea.Cmd) {
	var command tea.Cmd
	for _, char := range text {
		var updated tea.Model
		updated, command = model.Update(tea.KeyPressMsg{Code: char, Text: string(char)})
		model = updated.(Model)
	}
	return model, command
}

func TestAtMentionPickerSearchesInsertsAndAttachesFiles(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFiles(t, root, map[string]int{"go.mod": 3, "cmd/agent/main.go": 3, "README.md": 3})
	backend := &imageFakeBackend{called: make(chan struct{})}
	model := NewModel(backend, Config{Context: context.Background(), Workspace: root, AgentDir: t.TempDir(), NoColor: true})
	model.width, model.height = 80, 24

	model, _ = typeText(model, "看 @g")
	if !model.fileIndex.loading || model.mention == nil || !strings.Contains(model.View().Content, "正在索引工作区文件") {
		t.Fatalf("typing @ should start indexing: loading=%v mention=%#v", model.fileIndex.loading, model.mention)
	}
	updated, _ := model.Update(indexWorkspaceFiles(root)())
	model = updated.(Model)
	view := model.View().Content
	if !strings.Contains(view, "工作区文件") || !strings.Contains(view, "› go.mod") || !strings.Contains(view, "main.go") || !strings.Contains(view, "cmd/agent/") {
		t.Fatalf("file picker view = %q", view)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if model.suggestions[model.suggested].value != "cmd/agent/main.go" {
		t.Fatalf("down selected %q", model.suggestions[model.suggested].value)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.active != nil || model.input.displayValue() != "看 @go.mod " || model.panelOpen {
		t.Fatalf("enter should insert the file: active=%v input=%q panel=%v", model.active, model.input.displayValue(), model.panelOpen)
	}

	model, _ = typeText(model, "@REA")
	if !model.panelOpen || len(model.suggestions) != 1 {
		t.Fatalf("second mention suggestions = %#v", model.suggestions)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	model, _ = typeText(model, "D")
	if model.panelOpen {
		t.Fatal("Esc should keep the picker closed for the dismissed token")
	}
	model, _ = typeText(model, "ME.md")
	commands := model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if model.active == nil || len(commands) != 1 {
		t.Fatal("enter without an open picker should send the message")
	}
	for _, command := range commands[0]().(tea.BatchMsg) {
		if command != nil {
			go command()
		}
	}
	select {
	case <-backend.called:
	case <-time.After(5 * time.Second):
		t.Fatal("attachment backend was not called")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.sent != "看 @go.mod @README.md" || strings.Join(backend.files, "|") != "go.mod|README.md" {
		t.Fatalf("sent %q with files %q", backend.sent, backend.files)
	}
}

func TestTabInsertsMentionAndStaleIndexRefreshes(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceFiles(t, root, map[string]int{"docs/design notes.md": 1})
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: root, AgentDir: t.TempDir(), NoColor: true})
	model.fileIndex = workspaceFileIndex{files: []string{"docs/design notes.md"}, loadedAt: time.Now()}
	model, _ = typeText(model, "@design")
	if model.fileIndex.loading {
		t.Fatal("a fresh index should be reused")
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if model.input.displayValue() != `@"docs/design notes.md" ` {
		t.Fatalf("tab insert = %q", model.input.displayValue())
	}
	model.fileIndex.loadedAt = time.Now().Add(-2 * fileIndexTTL)
	model, _ = typeText(model, "@")
	if !model.fileIndex.loading {
		t.Fatal("a stale index should be refreshed when a new mention starts")
	}
}
