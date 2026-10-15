package workspace

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"bqagent/internal/globalconfig"
)

//go:embed defaults
var defaultFiles embed.FS

// retiredDefaults holds the SHA-256 of every previously shipped version of a
// default context file. A file still byte-identical to one of them was never
// edited by the user, so it is upgraded to the current default.
var retiredDefaults = map[string][]string{
	agentDocFileName: {
		"3e761621c9c84fa2864e335a56b3e20d0513f9755e6488891a18f6128d68dfb2",
		"4b4a8d81c7e1da0f3bf54ce02e5cf4b3699c38f6dcf45ece7839c5bc615ee4ab",
		"5bf8a285962de2c7a1d6ce580aeba9f6ab2da76745608ef1849ca7e396c14688",
		"8fc362e4c7322de5684663331bd80acea72a6b87b43f1fc8b95cf7a6562d2410",
		"bf54b2f014696aedc7a427a8159b465a467233c1cf8d2be10f1e3b4661b06c29",
		"de3d11b9c040f9a1d4afe68025c1dba4a3467af6ace9d59a1a115f55f4b815c4",
	},
	soulDocFileName: {
		"0b05cc77a2c7b4240fa44f3ac64deb3338a4ce9b1d76336892c38f35ffb4ceb8",
		"3b855e970d8567f695aef1a702c5a751f021e8d1a1663a49a63aef0d1ccc9158",
		"4cd1ba9d07ce31086ff8f3532efa79a444065278aa8f578e82f502f11431a263",
		"e07f89c949f1e053478d65ca0ea52b5eae6f4f612254d572d55ff04e8151c84a",
	},
}

// shouldWriteDefault reports whether target is missing or still an unedited
// retired default. Customized files are never overwritten.
func shouldWriteDefault(root *os.Root, target, name string) (bool, error) {
	info, err := root.Lstat(target)
	if os.IsNotExist(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	hashes := retiredDefaults[name]
	if len(hashes) == 0 || !info.Mode().IsRegular() {
		return false, nil
	}
	content, err := root.ReadFile(target)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(content)
	for _, hash := range hashes {
		if hex.EncodeToString(sum[:]) == hash {
			return true, nil
		}
	}
	return false, nil
}

// EnsureDefaults checks for missing global .agent context files and creates them from
// embedded defaults. Customized files are never overwritten; unedited copies of
// retired defaults are upgraded.
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
	if err := globalconfig.NewStore(agentDir).EnsureDefaults(); err != nil {
		return err
	}

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
		if targetPath == "config.json" {
			return nil
		}

		if d.IsDir() {
			return root.MkdirAll(targetPath, 0o755)
		}

		if write, err := shouldWriteDefault(root, targetPath, targetPath); err != nil || !write {
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
		if write, statErr := shouldWriteDefault(root, target, name); statErr != nil {
			return statErr
		} else if !write {
			continue
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
