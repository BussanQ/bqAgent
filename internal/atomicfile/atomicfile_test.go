package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceOverwritesTargetWithoutDeletingSiblingBackup(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	backup := target + ".bak"
	if err := os.WriteFile(source, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("user backup"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Replace(source, target); err != nil {
		t.Fatalf("Replace returned error: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("target = %q, want replacement", content)
	}
	backupContent, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("target.bak was deleted: %v", err)
	}
	if string(backupContent) != "user backup" {
		t.Fatalf("target.bak = %q, want user backup", backupContent)
	}
}

func TestWriteRootReplacesTargetAndCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	root := openRoot(t, dir)
	defer root.Close()

	const target = "nested/target.txt"
	if err := WriteRoot(root, target, []byte("first"), 0o640); err != nil {
		t.Fatalf("first WriteRoot: %v", err)
	}
	if err := WriteRoot(root, target, []byte("replacement"), 0o640); err != nil {
		t.Fatalf("replacement WriteRoot: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "nested", "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "replacement" {
		t.Fatalf("target = %q, want replacement", content)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "target.txt" {
		t.Fatalf("directory entries = %v, want only target.txt", entries)
	}
}

func TestWriteRootRemovesTemporaryFileAfterWriterError(t *testing.T) {
	dir := t.TempDir()
	root := openRoot(t, dir)
	defer root.Close()
	want := errors.New("writer failed")

	err := WriteRootFunc(root, "nested/target.txt", 0o644, func(io.Writer) error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("WriteRootFunc error = %v, want %v", err, want)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary file was not removed: %v", entries)
	}
}

func TestWriteRootAllowsInternalRelativeSymlink(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(dir, "link")); err != nil {
		t.Skipf("creating symlink is unavailable, commonly without Windows developer privileges: %v", err)
	}
	root := openRoot(t, dir)
	defer root.Close()

	if err := WriteRoot(root, filepath.Join("link", "target.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatalf("WriteRoot through internal symlink: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(inside, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "inside" {
		t.Fatalf("target = %q, want inside", content)
	}
}

func TestWriteRootRejectsStaticSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("creating symlink is unavailable, commonly without Windows developer privileges: %v", err)
	}
	root := openRoot(t, dir)
	defer root.Close()

	if err := WriteRoot(root, filepath.Join("escape", "secret.txt"), []byte("secret"), 0o644); err == nil {
		t.Fatal("WriteRoot accepted a path through an escaping symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped root; outside target stat error = %v, want not exist", err)
	}
}

func TestWriteRootRejectsSymlinkEscapeAfterRootOpened(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	parent := filepath.Join(dir, "target")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	root := openRoot(t, dir)
	defer root.Close()
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("creating symlink is unavailable, commonly without Windows developer privileges: %v", err)
	}

	if err := WriteRoot(root, filepath.Join("target", "secret.txt"), []byte("secret"), 0o644); err == nil {
		t.Fatal("WriteRoot accepted a symlink substituted after root was opened")
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped root; outside target stat error = %v, want not exist", err)
	}
}

func openRoot(t *testing.T, path string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("OpenRoot(%q): %v", path, err)
	}
	return root
}
