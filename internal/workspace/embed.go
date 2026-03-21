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
	return fs.WalkDir(defaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// "defaults/AGENT.md" → "AGENT.md"
		relPath := strings.TrimPrefix(path, "defaults/")
		if relPath == "" || relPath == "defaults" {
			return nil
		}

		targetPath := filepath.Join(w.ContextDir(), relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		if _, err := os.Stat(targetPath); err == nil {
			return nil // already exists, skip
		}

		content, err := defaultFiles.ReadFile(path)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(targetPath, content, 0o644)
	})
}
