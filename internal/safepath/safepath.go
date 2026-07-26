// Package safepath validates path components and resolves paths inside a root.
package safepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateComponent accepts exactly one filesystem path component. It rejects
// empty, dot, parent, and separator-containing values.
func ValidateComponent(value string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("path component %q is not valid", value)
	}
	if strings.ContainsAny(value, `/\\`) || filepath.Base(value) != value {
		return fmt.Errorf("path component %q must not contain a path separator", value)
	}
	return nil
}

// Relative lexically normalizes root and candidate, then returns the cleaned
// absolute root and a path relative to it. The candidate may be absolute or
// relative, but it must remain inside root after lexical normalization.
//
// Relative intentionally does not inspect the filesystem. Callers that perform
// filesystem operations should use an os.Root opened on the returned root and
// the returned relative path so symlink checks are performed by the operation
// itself and are not vulnerable to a check-then-use race.
func Relative(root, candidate string) (rootAbs, relative string, err error) {
	rootAbs, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	candidate = filepath.Clean(candidate)
	if !contained(rootAbs, candidate) {
		return "", "", fmt.Errorf("path %q is outside root %q", candidate, rootAbs)
	}
	relative, err = filepath.Rel(rootAbs, candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve relative path: %w", err)
	}
	return rootAbs, relative, nil
}

// Resolve resolves candidate beneath root. The candidate may be absolute or
// relative. In addition to lexical containment, the nearest existing ancestor
// is physically resolved and must remain beneath the physically resolved root.
// This permits a new final path component while rejecting static symlink
// escapes. Filesystem operations that need TOCTOU protection should instead
// use Relative with os.Root.
func Resolve(root, candidate string) (string, error) {
	rootAbs, relative, err := Relative(root, candidate)
	if err != nil {
		return "", err
	}
	candidate = filepath.Join(rootAbs, relative)
	if err := ensurePhysicalContainment(rootAbs, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func ensurePhysicalContainment(root, candidate string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	existing := candidate
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil
		}
		existing = parent
	}
	resolvedExisting, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return err
	}
	if !contained(resolvedRoot, resolvedExisting) {
		return fmt.Errorf("resolved path %q is not under %q", resolvedExisting, resolvedRoot)
	}
	return nil
}

func contained(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
