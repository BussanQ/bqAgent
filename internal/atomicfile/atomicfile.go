// Package atomicfile provides durable same-directory atomic file replacement.
package atomicfile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const temporaryFileAttempts = 100

// Write atomically replaces path with content, creating its parent directory.
func Write(path string, content []byte, mode os.FileMode) error {
	return WriteFunc(path, mode, func(file io.Writer) error {
		_, err := file.Write(content)
		return err
	})
}

// WriteFunc atomically replaces path with data written by write, creating its
// parent directory. The temporary file is synced before replacement.
func WriteFunc(path string, mode os.FileMode, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return WriteRootFunc(root, filepath.Base(path), mode, write)
}

// WriteRoot atomically replaces path relative to root with content. Its parent
// directory is created when needed. All filesystem access is performed through
// root, so symlinks cannot escape the root even if they change after path
// validation.
func WriteRoot(root *os.Root, path string, content []byte, mode os.FileMode) error {
	return WriteRootFunc(root, path, mode, func(file io.Writer) error {
		_, err := file.Write(content)
		return err
	})
}

// WriteRootFunc atomically replaces path relative to root with data written by
// write. It creates a random temporary file in the target's directory with
// O_EXCL, syncs it, and renames it through root after closing it.
func WriteRootFunc(root *os.Root, path string, mode os.FileMode, write func(io.Writer) error) error {
	if root == nil {
		return fmt.Errorf("atomic write root is nil")
	}
	if write == nil {
		return fmt.Errorf("atomic write callback is nil")
	}

	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if err := ensureParent(root, parent); err != nil {
		return err
	}

	temporary, file, err := createTemp(root, parent, filepath.Base(path), mode)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)

	if err := write(file); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(root, parent)
}

func ensureParent(root *os.Root, parent string) error {
	if parent == "." {
		return nil
	}
	dir, err := root.OpenRoot(parent)
	if err == nil {
		return dir.Close()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.MkdirAll(parent, 0o755); err != nil {
		// MkdirAll reports an existing symlink as an existence error. Reopening
		// through Root distinguishes a safe internal link from an escaping one.
		dir, openErr := root.OpenRoot(parent)
		if openErr != nil {
			return openErr
		}
		return dir.Close()
	}
	dir, err = root.OpenRoot(parent)
	if err != nil {
		return err
	}
	return dir.Close()
}

func createTemp(root *os.Root, parent, base string, mode os.FileMode) (string, *os.File, error) {
	for range temporaryFileAttempts {
		random, err := randomSuffix()
		if err != nil {
			return "", nil, fmt.Errorf("generate temporary file name: %w", err)
		}
		temporary := filepath.Join(parent, "."+base+"-"+random+".tmp")
		file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			return temporary, file, nil
		}
		if os.IsExist(err) {
			continue
		}
		return "", nil, err
	}
	return "", nil, fmt.Errorf("create temporary file for %q: too many name collisions", base)
}

func randomSuffix() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func syncDirectory(root *os.Root, path string) error {
	// Windows' rename is write-through; opening a directory for Sync is denied.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := root.Open(path)
	if err != nil {
		return fmt.Errorf("open parent directory for %s: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync parent directory for %s: %w", path, err)
	}
	return nil
}
