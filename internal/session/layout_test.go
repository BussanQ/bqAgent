package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceDirectoryNameIsBoundedAndDistinct(t *testing.T) {
	base := t.TempDir()
	first := WorkspaceDirectoryName(filepath.Join(base, "a", "b"))
	second := WorkspaceDirectoryName(filepath.Join(base, "a-b"))
	if first == second {
		t.Fatal("different paths have the same directory name")
	}
	if first != WorkspaceDirectoryName(filepath.Join(base, "a")+"/./b/") {
		t.Fatal("equivalent clean paths have different directory names")
	}
	for _, path := range []string{base, "/", filepath.Join(base, strings.Repeat("长路径", 200)), filepath.Join(base, strings.Repeat("a", 400))} {
		name := WorkspaceDirectoryName(path)
		if len(name) > 113 || strings.ContainsAny(name, `/\`) || name == "" {
			t.Fatalf("unsafe or unbounded workspace name: %q", name)
		}
	}
}

func moveSessionToLegacyLayout(t *testing.T, saved *Session) string {
	t.Helper()
	legacy, err := saved.store.legacySessionDir(saved.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved.Dir(), legacy); err != nil {
		t.Fatal(err)
	}
	return legacy
}

func TestWorkspaceLayoutResumesListsMaintainsAndDeletesLegacySessions(t *testing.T) {
	options := DefaultOptions()
	options.AgentDir = filepath.Join(t.TempDir(), ".agent")
	options.OutputMaxBytes = 12
	store := NewStore(t.TempDir(), options)
	legacy, err := store.Create(CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.RecordMessage(map[string]any{"role": "user", "content": "resume this"}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.SetCurrentModel("saved-model"); err != nil {
		t.Fatal(err)
	}
	legacyDir := moveSessionToLegacyLayout(t, legacy)
	reopened, err := store.Open(legacy.ID())
	if err != nil {
		t.Fatal(err)
	}
	messages, err := reopened.LoadMessages()
	if err != nil || len(messages) != 1 || messages[0]["content"] != "resume this" || reopened.Meta().CurrentModel != "saved-model" || reopened.Dir() != legacyDir {
		t.Fatalf("legacy resume lost state: messages=%v, meta=%+v, dir=%s, err=%v", messages, reopened.Meta(), reopened.Dir(), err)
	}
	// Updating old sessions must not move or recreate them in the new layout.
	if err := reopened.SetFastMode(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy.Dir()); !os.IsNotExist(err) {
		t.Fatalf("legacy update created a new-layout directory: %v", err)
	}
	newSession, err := store.Create(CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	newSession.meta.UpdatedAt = time.Now().Add(time.Hour)
	if err := newSession.persistMeta(); err != nil {
		t.Fatal(err)
	}
	foreignStore := NewStore(t.TempDir(), options)
	foreign, err := foreignStore.Create(CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	foreignDir := moveSessionToLegacyLayout(t, foreign)
	if _, err := store.Open(foreign.ID()); !errors.Is(err, ErrWorkspaceMismatch) {
		t.Fatalf("foreign legacy session error = %v", err)
	}
	for _, dir := range []string{legacyDir, newSession.Dir(), foreignDir} {
		if err := os.WriteFile(filepath.Join(dir, "output.log"), []byte("old output that must go\nlatest\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.List(0)
	if err != nil || len(listed) != 2 || listed[0].ID != newSession.ID() {
		t.Fatalf("mixed layout listing = %+v, %v", listed, err)
	}
	limited, err := store.List(1)
	if err != nil || len(limited) != 1 || limited[0].ID != newSession.ID() {
		t.Fatalf("limited listing = %+v, %v", limited, err)
	}
	if errs := store.MaintainExistingSessions(); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, dir := range []string{legacyDir, newSession.Dir()} {
		data, err := os.ReadFile(filepath.Join(dir, "output.log"))
		if err != nil || string(data) != "latest\n" {
			t.Fatalf("maintenance at %s: %q, %v", dir, data, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(foreignDir, "output.log"))
	if err != nil || !strings.Contains(string(data), "old output") {
		t.Fatalf("foreign log was changed: %q, %v", data, err)
	}
	if err := store.Delete(foreign.ID()); !errors.Is(err, ErrWorkspaceMismatch) {
		t.Fatalf("foreign legacy delete = %v", err)
	}
	if err := store.Delete(legacy.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy directory remains: %v", err)
	}
	if _, err := store.Open(newSession.ID()); err != nil {
		t.Fatalf("new session was affected: %v", err)
	}
}

func TestWorkspaceLayoutPrefersNewDirectoryAndDoesNotMaskCorruption(t *testing.T) {
	store := NewStore(t.TempDir())
	saved, err := store.Create(CreateOptions{Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	legacyDir := moveSessionToLegacyLayout(t, saved)
	if err := saved.persistMeta(); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(0)
	if err != nil || len(listed) != 1 {
		t.Fatalf("duplicate layout listing = %+v, %v", listed, err)
	}
	reopened, err := store.Open(saved.ID())
	if err != nil || reopened.Dir() != saved.Dir() {
		t.Fatalf("new layout not preferred: %v", err)
	}
	if err := os.Remove(filepath.Join(saved.Dir(), "meta.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(saved.ID()); !os.IsNotExist(err) {
		t.Fatalf("damaged new session fell back to stale legacy metadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "meta.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLayoutRejectsSymlinkEscape(t *testing.T) {
	store := NewStore(t.TempDir())
	root := filepath.Join(store.agentDir, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, WorkspaceDirectoryName(store.workspaceRoot))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(CreateOptions{}); err == nil {
		t.Fatal("Create accepted an escaping workspace symlink")
	}
	if _, err := store.Open("demo"); err == nil {
		t.Fatal("Open accepted an escaping workspace symlink")
	}
	if _, err := store.List(0); err == nil {
		t.Fatal("List accepted an escaping workspace symlink")
	}
	if errs := store.MaintainExistingSessions(); len(errs) == 0 {
		t.Fatal("maintenance accepted an escaping workspace symlink")
	}
}
