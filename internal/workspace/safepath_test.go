package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildSystemPromptRejectsEscapingContextSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "AGENT.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, ".agent", "AGENT.md"))
	_, err := (&Workspace{Root: root}).BuildSystemPrompt("base")
	if err == nil {
		t.Fatal("BuildSystemPrompt accepted an escaping context symlink")
	}
}

func TestBuildSystemPromptRejectsEscapingMemoryDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, ".agent", "memory"))
	_, err := (&Workspace{Root: root}).BuildSystemPrompt("base")
	if err == nil {
		t.Fatal("BuildSystemPrompt accepted an escaping memory symlink")
	}
}

func TestEnsureDefaultsRejectsEscapingAgentDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(root, ".agent"))
	if err := (&Workspace{Root: root}).EnsureDefaults(); err == nil {
		t.Fatal("EnsureDefaults accepted an escaping .agent symlink")
	}
}

func TestRootFileOperationsRejectEscapingDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, "link"))
	ws := &Workspace{Root: root}

	if _, err := ws.readFileInsideRoot(filepath.Join(root, "link", "secret.md")); err == nil {
		t.Fatal("readFileInsideRoot accepted an escaping symlink")
	}
	if _, err := ws.readDirInsideRoot(filepath.Join(root, "link")); err == nil {
		t.Fatal("readDirInsideRoot accepted an escaping symlink")
	}
	if err := ws.mkdirAllInsideRoot(filepath.Join(root, "link", "created"), 0o755); err == nil {
		t.Fatal("mkdirAllInsideRoot accepted an escaping symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "created")); !os.IsNotExist(err) {
		t.Fatalf("escaping mkdir created outside directory: %v", err)
	}
}

func TestAppendMemoryRejectsEscapingMemorySymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, ".agent", "memory"))

	if err := (&Workspace{Root: root}).AppendMemory("task", "result"); err == nil {
		t.Fatal("AppendMemory accepted an escaping memory symlink")
	}
}

func TestReadFileRejectsSymlinkSwappedAfterRootOpen(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "value.md"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "value.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	symlinkOrSkip(t, "inside", link)

	ws := &Workspace{Root: root}
	workspaceRoot, err := ws.openRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer workspaceRoot.Close()
	path, err := ws.pathInsideRoot(filepath.Join(link, "value.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, link)

	if _, err := workspaceRoot.ReadFile(path); err == nil {
		t.Fatal("Root.ReadFile accepted a symlink changed to escape after path validation")
	}
}

func TestBuildSystemPromptAllowsInternalRelativeSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "source.md"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, "source.md", filepath.Join(root, ".agent", "AGENT.md"))

	prompt, err := (&Workspace{Root: root}).BuildSystemPrompt("base")
	if err != nil {
		t.Fatalf("BuildSystemPrompt returned error for an internal symlink: %v", err)
	}
	if !strings.Contains(prompt, "inside") {
		t.Fatalf("prompt = %q, want internal symlink content", prompt)
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlink requires Windows privileges: %v", err)
		}
		t.Fatalf("creating symlink: %v", err)
	}
}
