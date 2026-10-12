package safepath

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenLinkedSkill treats a directory link installed directly under
// .agent/skills as an explicit, read-only skill mount. Ancestor directories
// remain confined to root; subsequent reads are confined to the target root.
// Ordinary directories return nil so callers retain their existing behavior.
func OpenLinkedSkill(root, name string) (*os.Root, error) {
	if err := ValidateComponent(name); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	skills, err := base.OpenRoot(filepath.Join(".agent", "skills"))
	if err != nil {
		return nil, err
	}
	defer skills.Close()
	info, err := skills.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil, nil
	}
	target, err := skills.Readlink(name)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(target) {
		parent, err := filepath.EvalSymlinks(filepath.Join(absolute, ".agent", "skills"))
		if err != nil {
			return nil, err
		}
		target = filepath.Join(parent, target)
	}
	linked, err := os.OpenRoot(target)
	if err != nil {
		return nil, err
	}
	info, err = linked.Stat("SKILL.md")
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("linked skill %q has no regular SKILL.md", name)
	}
	if err != nil {
		linked.Close()
		return nil, err
	}
	return linked, nil
}
