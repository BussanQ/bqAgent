package privatefile

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func skipWithoutUnixPermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func TestWriteFileCreatesPrivateFileAndDirectory(t *testing.T) {
	skipWithoutUnixPermissions(t)
	dir := filepath.Join(t.TempDir(), "state", "chats")
	path := filepath.Join(dir, "peer.json")
	if err := WriteFile(path, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, FileMode)
	assertMode(t, dir, DirMode)
}

func TestWriteFileTightensExistingFileAndDirectory(t *testing.T) {
	skipWithoutUnixPermissions(t)
	dir := filepath.Join(t.TempDir(), "legacy")
	path := filepath.Join(dir, "state.json")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFunc(path, func(w io.Writer) error { _, err := w.Write([]byte("new")); return err }); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, FileMode)
	assertMode(t, dir, DirMode)
}

func TestOpenFileTightensExistingAppendTarget(t *testing.T) {
	skipWithoutUnixPermissions(t)
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := OpenFile(path, os.O_WRONLY|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, FileMode)
}

func TestMkdirAllLeavesExistingParentsUnchanged(t *testing.T) {
	skipWithoutUnixPermissions(t)
	parent := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "private")
	if err := MkdirAll(child); err != nil {
		t.Fatal(err)
	}
	assertMode(t, parent, 0o755)
	assertMode(t, child, DirMode)
}
