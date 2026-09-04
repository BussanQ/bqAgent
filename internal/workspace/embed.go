package workspace

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed defaults
var defaultFiles embed.FS

// EnsureDefaults checks for missing global .agent context files and creates them from
// embedded defaults. Existing files are never overwritten.
func (w *Workspace) EnsureDefaults() error {
	agentDir := w.AgentDir()
	if info, err := os.Lstat(agentDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("agent directory must not be a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(agentDir)
	if err != nil {
		return err
	}
	defer root.Close()

	return fs.WalkDir(defaultFiles, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// "defaults/AGENT.md" → "~/.agent/AGENT.md"
		relPath := strings.TrimPrefix(path, "defaults/")
		if relPath == "" || relPath == "defaults" {
			return nil
		}
		targetPath := filepath.Clean(relPath)

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
		mode := fs.FileMode(0o644)
		if targetPath == "config.json" {
			mode = 0o600
		}
		return root.WriteFile(targetPath, content, mode)
	})
}

// EnsureLocalConfig creates the opt-in workspace configuration layer. It is
// intentionally limited to files that can augment the global configuration.
func (w *Workspace) EnsureLocalConfig() error {
	root, err := w.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	if err := root.MkdirAll(filepath.Join(agentDirName, memoryDirName), 0o755); err != nil {
		return err
	}
	for _, name := range []string{mcpConfigFileName, agentDocFileName, soulDocFileName, userDocFileName} {
		target := filepath.Join(agentDirName, name)
		if _, statErr := root.Stat(target); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		content, readErr := defaultFiles.ReadFile(filepath.Join("defaults", name))
		if readErr != nil {
			return readErr
		}
		if writeErr := root.WriteFile(target, content, 0o644); writeErr != nil {
			return writeErr
		}
	}
	return nil
}
