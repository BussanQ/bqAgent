//go:build windows

package atomicfile

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Replace uses Windows' overwrite-aware move operation. Unlike a rename-to-.bak
// fallback, it never touches an unrelated target.bak file.
func Replace(source, target string) error {
	if err := windows.MoveFileEx(
		windows.StringToUTF16Ptr(source),
		windows.StringToUTF16Ptr(target),
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}
