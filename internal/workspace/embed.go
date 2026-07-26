package workspace

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed defaults
var defaultFiles embed.FS

// EnsureDefaults checks for missing .gent context files and creates them from
// embedded defaults. Existing files are never overwritten.
func (w *Workspace) EnsureDefaults() error {
	root, err := w.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	return fs.WalkDir(defaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// "defaults/AGENT.md" → ".agent/AGENT.md"
		relPath := strings.TrimPrefix(path, "defaults/")
		if relPath == "" || relPath == "defaults" {
			return nil
		}
		targetPath := filepath.Join(contextDirName, relPath)

		if d.IsDir() {
			return root.MkdirAll(targetPath, 0o755)
		}

		if _, err := root.Stat(targetPath); err == nil {
			return nil // already exists, skip
		} else if !os.IsNotExist(err) {
			return err
		}

		content, err := defaultFiles.ReadFile(path)
		if err != nil {
			return err
		}

		if err := root.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return root.WriteFile(targetPath, content, 0o644)
	})
}
