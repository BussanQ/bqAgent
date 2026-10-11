package session

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bqagent/internal/safepath"
)

// WorkspaceDirectoryName groups sessions by the cleaned absolute workspace
// path. The bounded readable suffix is for people; the hash distinguishes paths
// that sanitize to the same name. Symlinks are deliberately not resolved here,
// matching the store's lexical workspace ownership checks.
func WorkspaceDirectoryName(workspaceRoot string) string {
	path := filepath.Clean(workspaceRoot)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	hash := sha256.Sum256([]byte(path))
	readable := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, path)
	if len(readable) > 96 {
		readable = readable[len(readable)-96:]
	}
	readable = strings.Trim(readable, "-")
	if readable == "" {
		readable = "workspace"
	}
	return fmt.Sprintf("%s-%x", readable, hash[:8])
}

func (s *Store) legacySessionDir(id string) (string, error) {
	if _, err := CanonicalID(id); err != nil {
		return "", err
	}
	return safepath.Resolve(filepath.Dir(s.agentDir), filepath.Join(filepath.Base(s.agentDir), "sessions", id))
}

// sessionIDs enumerates the current workspace and old flat sessions, without
// descending into other workspaces. New-layout entries take precedence.
func (s *Store) sessionIDs() ([]string, error) {
	ids := []string{}
	seen := make(map[string]bool)
	for _, group := range []string{WorkspaceDirectoryName(s.workspaceRoot), ""} {
		root, err := safepath.Resolve(filepath.Dir(s.agentDir), filepath.Join(filepath.Base(s.agentDir), "sessions", group))
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || seen[entry.Name()] {
				continue
			}
			if group == "" {
				// Workspace containers have no session metadata at this level.
				if _, err := os.Lstat(filepath.Join(root, entry.Name(), "meta.json")); os.IsNotExist(err) {
					continue
				} else if err != nil {
					return nil, err
				}
			}
			seen[entry.Name()] = true
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}
