//go:build !windows

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Replace renames source over target and syncs the containing directory so the
// replacement is durable after a crash.
func Replace(source, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("open parent directory for %s: %w", target, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync parent directory for %s: %w", target, err)
	}
	return nil
}
