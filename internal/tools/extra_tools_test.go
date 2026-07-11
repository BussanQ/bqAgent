package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(path, []byte("l1\nl2\nl3\nl4\nl5"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := ReadFileFromRoot(dir)

	full, err := read(context.Background(), map[string]any{"path": "lines.txt"})
	if err != nil || full != "l1\nl2\nl3\nl4\nl5" {
		t.Fatalf("full read = %q, err = %v", full, err)
	}
	part, err := read(context.Background(), map[string]any{"path": "lines.txt", "offset": "2", "limit": "2"})
	if err != nil {
		t.Fatalf("partial read error: %v", err)
	}
	if part != "l2\nl3" {
		t.Fatalf("partial read = %q, want %q", part, "l2\nl3")
	}
	if _, err := read(context.Background(), map[string]any{"path": "lines.txt", "limit": "-1"}); err == nil {
		t.Fatal("negative limit should error")
	}
}

func TestWorkspacePathsNormalizeInsideAndRejectOutside(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := ReadFileFromRoot(root)
	content, err := read(context.Background(), map[string]any{"path": inside})
	if err != nil || content != "inside" {
		t.Fatalf("absolute inside read = %q, err = %v", content, err)
	}

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := read(context.Background(), map[string]any{"path": outside}); err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("outside path error = %v, want clear workspace rejection", err)
	}
	if _, err := read(context.Background(), map[string]any{"path": "/Users/example/project/main.go"}); err == nil || (!strings.Contains(err.Error(), "another platform") && !strings.Contains(err.Error(), "outside workspace")) {
		t.Fatalf("foreign absolute path error = %v, want fast actionable rejection", err)
	}
}

func TestGlobAcceptsAbsoluteWorkspacePatternAndReturnsRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "main.go"), []byte("package internal"), 0o644); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.ToSlash(filepath.Join(root, "**", "*.go"))
	out, err := GlobInRoot(root)(context.Background(), map[string]any{"pattern": pattern})
	if err != nil {
		t.Fatalf("absolute workspace glob error: %v", err)
	}
	if strings.TrimSpace(out) != "internal/main.go" {
		t.Fatalf("glob output = %q, want workspace-relative path", out)
	}
}

func TestEditFileUniqueReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("alpha beta gamma"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := EditFileInRoot(dir)

	if _, err := edit(context.Background(), map[string]any{"path": "f.txt", "old_string": "beta", "new_string": "BETA"}); err != nil {
		t.Fatalf("edit error: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "alpha BETA gamma" {
		t.Fatalf("content = %q", string(got))
	}
}

func TestEditFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x x x"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := EditFileInRoot(dir)

	if _, err := edit(context.Background(), map[string]any{"path": "f.txt", "old_string": "x", "new_string": "y"}); err == nil {
		t.Fatal("non-unique match should error without replace_all")
	}
	if _, err := edit(context.Background(), map[string]any{"path": "f.txt", "old_string": "zzz", "new_string": "y"}); err == nil {
		t.Fatal("missing old_string should error")
	}
	if _, err := edit(context.Background(), map[string]any{"path": "f.txt", "old_string": "x", "new_string": "x"}); err == nil {
		t.Fatal("identical old/new should error")
	}

	result, err := edit(context.Background(), map[string]any{"path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": "true"})
	if err != nil {
		t.Fatalf("replace_all error: %v", err)
	}
	if !strings.Contains(result, "3 replacement") {
		t.Fatalf("result = %q, want 3 replacements", result)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "y y y" {
		t.Fatalf("content = %q", string(got))
	}
}

func TestGrepFindsMatchesAndFilters(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nfunc Target() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("Target here too\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "c.go"), []byte("Target in git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	grep := GrepInRoot(dir)

	out, err := grep(context.Background(), map[string]any{"pattern": "Target", "glob": "*.go"})
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}
	if !strings.Contains(out, "a.go:2:func Target") {
		t.Fatalf("grep output = %q, want a.go match with line number", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Fatalf("glob filter failed, .txt matched: %q", out)
	}
	if strings.Contains(out, ".git") {
		t.Fatalf(".git should be skipped: %q", out)
	}

	if _, err := grep(context.Background(), map[string]any{"pattern": "("}); err == nil {
		t.Fatal("invalid regexp should error")
	}
}

func TestGlobMatchesDoubleStar(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "deep", "nested.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	glob := GlobInRoot(dir)

	out, err := glob(context.Background(), map[string]any{"pattern": "**/*.go"})
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}
	if !strings.Contains(out, "root.go") || !strings.Contains(out, "sub/deep/nested.go") {
		t.Fatalf("glob output = %q, want both .go files", out)
	}
	if strings.Contains(out, "note.txt") {
		t.Fatalf("glob matched non-.go file: %q", out)
	}
}

func TestTodoWriteUpdatesStore(t *testing.T) {
	store := NewTodoStore()
	todo := TodoWriteWithStore(store)

	out, err := todo(context.Background(), map[string]any{"todos": `[{"content":"do A","status":"in_progress","activeForm":"Doing A"},{"content":"do B","status":"pending"}]`})
	if err != nil {
		t.Fatalf("todo_write error: %v", err)
	}
	if !strings.Contains(out, "do A") || !strings.Contains(out, "[~]") {
		t.Fatalf("rendered output = %q", out)
	}
	if len(store.items) != 2 {
		t.Fatalf("store items = %d, want 2", len(store.items))
	}

	if _, err := todo(context.Background(), map[string]any{"todos": "not json"}); err == nil {
		t.Fatal("invalid JSON should error")
	}
	if _, err := todo(context.Background(), map[string]any{"todos": `[{"content":"x","status":"bogus"}]`}); err == nil {
		t.Fatal("invalid status should error")
	}
}
