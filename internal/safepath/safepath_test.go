package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateComponentRejectsPathSyntax(t *testing.T) {
	for _, value := range []string{"", ".", "..", "a/b", `a\\b`} {
		if err := ValidateComponent(value); err == nil {
			t.Errorf("ValidateComponent(%q) succeeded, want error", value)
		}
	}
	if err := ValidateComponent("safe-id_1"); err != nil {
		t.Fatalf("ValidateComponent safe value: %v", err)
	}
}

func TestRelativeLexicallyNormalizesRootAndCandidate(t *testing.T) {
	root := t.TempDir()
	rootWithDots := filepath.Join(root, "child", "..")

	gotRoot, gotRelative, err := Relative(rootWithDots, filepath.Join("nested", "..", "file.txt"))
	if err != nil {
		t.Fatalf("Relative returned error: %v", err)
	}
	if gotRoot != filepath.Clean(root) {
		t.Errorf("root = %q, want %q", gotRoot, filepath.Clean(root))
	}
	if gotRelative != "file.txt" {
		t.Errorf("relative = %q, want file.txt", gotRelative)
	}
}

func TestRelativePreservesOutsideRootError(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "..", "outside.txt")
	if _, _, err := Relative(root, candidate); err == nil {
		t.Fatal("Relative accepted a path outside root")
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("creating symlink is unavailable, commonly without Windows developer privileges: %v", err)
	}
	if _, err := Resolve(root, filepath.Join("escape", "secret.txt")); err == nil {
		t.Fatal("Resolve accepted a path through an escaping symlink")
	}
}
