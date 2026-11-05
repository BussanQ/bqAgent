// Package privatefile creates and writes per-user state (session history,
// channel credentials and peer state) readable only by the owning user.
//
// Permissions are tightened only on create/write paths, never on reads: a
// state file owned by another user or on a read-only mount must still open.
// Tightening is best effort for the same reason; a failed chmod leaves the
// previous mode in place rather than failing the write.
package privatefile

import (
	"io"
	"os"
	"path/filepath"

	"bqagent/internal/atomicfile"
)

const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

// MkdirAll creates dir (and missing parents) with DirMode, then tightens dir
// itself if it already existed with group/other access. Existing parents are
// left unchanged because they may be shared, user-chosen directories.
func MkdirAll(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	tighten(dir, DirMode)
	return nil
}

// WriteFile atomically replaces path with content using FileMode, creating
// the parent directory with DirMode. Because the replacement is a freshly
// created file, a previously world-readable file becomes private.
func WriteFile(path string, content []byte) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return atomicfile.Write(path, content, FileMode)
}

// WriteFunc is WriteFile with streamed content.
func WriteFunc(path string, write func(io.Writer) error) error {
	if err := MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return atomicfile.WriteFunc(path, FileMode, write)
}

// OpenFile opens path for writing with FileMode for new files and tightens an
// existing file that still has group/other access. Callers must include a
// write flag; read-only opens should use os.Open so reads never chmod.
func OpenFile(path string, flag int) (*os.File, error) {
	file, err := os.OpenFile(path, flag, FileMode)
	if err != nil {
		return nil, err
	}
	if info, statErr := file.Stat(); statErr == nil && info.Mode().Perm()&^FileMode != 0 {
		_ = file.Chmod(FileMode)
	}
	return file, nil
}

func tighten(path string, mode os.FileMode) {
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&^mode == 0 {
		return
	}
	_ = os.Chmod(path, mode)
}
