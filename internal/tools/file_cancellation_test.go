package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileMutationsHonorCancellation(t *testing.T) {
	for _, rooted := range []bool{false, true} {
		for _, edit := range []bool{false, true} {
			dir := t.TempDir()
			path := filepath.Join(dir, "file.txt")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			root, argPath := "", path
			if rooted {
				root, argPath = dir, "file.txt"
			}
			fn := WriteFileToRoot(root)
			args := map[string]any{"path": argPath, "content": "changed"}
			if edit {
				fn = EditFileInRoot(root)
				args = map[string]any{"path": argPath, "old_string": "original", "new_string": "changed"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := fn(ctx, args); !errors.Is(err, context.Canceled) {
				t.Fatalf("rooted=%v edit=%v err=%v", rooted, edit, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "original" {
				t.Fatalf("cancelled mutation changed file: %q %v", data, err)
			}
		}
	}
}
