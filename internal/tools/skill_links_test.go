package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileSupportsLinkedSkillsAndKeepsTargetBoundary(t *testing.T) {
	workspace, agentDir, target := t.TempDir(), filepath.Join(t.TempDir(), ".agent"), t.TempDir()
	for _, dir := range []string{filepath.Join(agentDir, "skills"), filepath.Join(target, "references")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for path, text := range map[string]string{filepath.Join(target, "SKILL.md"): "linked skill", filepath.Join(target, "references", "guide.md"): "first\nsecond\nthird"} {
		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(agentDir, "skills", "demo")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	read := ReadFileFromWorkspaceAndAgent(workspace, agentDir, 1024)
	text, err := read(context.Background(), map[string]any{"path": ".agent/skills/demo/SKILL.md"})
	if err != nil || text != "linked skill" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	text, err = read(context.Background(), map[string]any{"path": ".agent/skills/demo/references/guide.md", "offset": float64(2), "limit": float64(1)})
	if err != nil || text != "second" {
		t.Fatalf("reference=%q err=%v", text, err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(target, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".agent/skills/demo/escape", filepath.Join(target, "SKILL.md")} {
		if _, err := read(context.Background(), map[string]any{"path": path}); err == nil {
			t.Fatalf("read escaped via %s", path)
		}
	}
	if _, err := WriteFileToRoot(filepath.Dir(agentDir))(context.Background(), map[string]any{"path": ".agent/skills/demo/SKILL.md", "content": "overwrite"}); err == nil {
		t.Fatal("skill mount allowed write outside root")
	}
	local := filepath.Join(workspace, ".agent", "skills")
	if err := os.MkdirAll(local, 0755); err != nil {
		t.Fatal(err)
	}
	localTarget := t.TempDir()
	if err := os.WriteFile(filepath.Join(localTarget, "SKILL.md"), []byte("local skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(localTarget, filepath.Join(local, "demo")); err != nil {
		t.Fatal(err)
	}
	text, err = read(context.Background(), map[string]any{"path": ".agent/skills/demo/SKILL.md"})
	if err != nil || text != "local skill" {
		t.Fatalf("local override=%q err=%v", text, err)
	}
	limited := ReadFileFromWorkspaceAndAgent(workspace, agentDir, 5)
	text, err = limited(context.Background(), map[string]any{"path": ".agent/skills/demo/SKILL.md"})
	if err != nil || !strings.Contains(text, "local") || strings.Contains(text, "local skill") {
		t.Fatalf("bounded read=%q err=%v", text, err)
	}
}
