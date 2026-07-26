package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (w *Workspace) openRoot() (*os.Root, error) {
	if w == nil {
		return nil, fmt.Errorf("workspace is nil")
	}
	root, err := filepath.Abs(w.Root)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(root)
}

func (w *Workspace) pathInsideRoot(path string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("workspace is nil")
	}
	root, err := filepath.Abs(w.Root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("workspace path %q escapes root", path)
	}
	return relative, nil
}

func (w *Workspace) readFileInsideRoot(path string) ([]byte, error) {
	root, err := w.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return w.readFileFromRoot(root, path)
}

func (w *Workspace) readFileFromRoot(root *os.Root, path string) ([]byte, error) {
	relative, err := w.pathInsideRoot(path)
	if err != nil {
		return nil, err
	}
	return root.ReadFile(relative)
}

func (w *Workspace) readDirInsideRoot(path string) ([]os.DirEntry, error) {
	root, err := w.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return w.readDirFromRoot(root, path)
}

func (w *Workspace) readDirFromRoot(root *os.Root, path string) ([]os.DirEntry, error) {
	relative, err := w.pathInsideRoot(path)
	if err != nil {
		return nil, err
	}
	dir, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
}

func (w *Workspace) mkdirAllInsideRoot(path string, mode os.FileMode) error {
	root, err := w.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return w.mkdirAllFromRoot(root, path, mode)
}

func (w *Workspace) mkdirAllFromRoot(root *os.Root, path string, mode os.FileMode) error {
	relative, err := w.pathInsideRoot(path)
	if err != nil {
		return err
	}
	return root.MkdirAll(relative, mode)
}
