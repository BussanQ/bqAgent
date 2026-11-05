package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSessionStateFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	store := NewStore(t.TempDir())
	saved, err := store.Create(CreateOptions{Task: "private"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a session written by an older version with world-readable modes.
	if err := os.Chmod(saved.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saved.MessagesPath(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(saved.MessagesPath(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := saved.RecordMessages(map[string]any{"role": "user", "content": "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := saved.SaveGroupConfig(GroupConfig{}); err != nil {
		t.Fatal(err)
	}
	output, err := saved.OpenOutputFile()
	if err != nil {
		t.Fatal(err)
	}
	_ = output.Close()

	for _, path := range []string{saved.MessagesPath(), filepath.Join(saved.Dir(), "meta.json"), saved.GroupConfigPath(), saved.OutputPath()} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("%s mode = %04o, want 0600", filepath.Base(path), mode)
		}
	}
	info, err := os.Stat(saved.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Fatalf("session dir mode = %04o, want 0700", mode)
	}
}
