package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverFindsNearestWorkspaceMarker(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("failed to create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("failed to create go.mod: %v", err)
	}

	ws, err := Discover(nested)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if ws.Root != root {
		t.Fatalf("workspace root = %q, want %q", ws.Root, root)
	}
}

func TestBuildSystemPromptIncludesRulesSkillsAndMemory(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}

	if err := os.MkdirAll(filepath.Join(root, ".agent", "rules"), 0o755); err != nil {
		t.Fatalf("failed to create rules directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skills directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "rules", "safety.md"), []byte("Always be careful."), 0o644); err != nil {
		t.Fatalf("failed to write rule file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("# Demo Skill\n\nHelps summarize repository changes."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent_memory.md"), []byte("old\nrecent memory"), 0o644); err != nil {
		t.Fatalf("failed to write memory file: %v", err)
	}

	prompt, err := ws.BuildSystemPrompt("Base prompt")
	if err != nil {
		t.Fatalf("BuildSystemPrompt returned error: %v", err)
	}

	checks := []string{
		"Base prompt",
		"# Workspace",
		"# Rules",
		"Always be careful.",
		"# Skills",
		"Demo Skill: Helps summarize repository changes.",
		"# Memory",
		"## agent_memory.md",
		"recent memory",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("prompt = %q, want substring %q", prompt, check)
		}
	}
}

func TestBuildSystemPromptIncludesWorkspaceDirectoryDocuments(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}

	originalNowFunc := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 3, 21, 10, 0, 0, 0, time.Local) }
	defer func() { nowFunc = originalNowFunc }()

	if err := os.MkdirAll(filepath.Join(root, ".agent", "memory"), 0o755); err != nil {
		t.Fatalf("failed to create .agent memory directory: %v", err)
	}
	files := map[string]string{
		filepath.Join(root, ".agent", "AGENT.md"):                "# AGENT\n\nUse memory carefully.",
		filepath.Join(root, ".agent", "SOUL.md"):                 "# SOUL\n\nBe direct.",
		filepath.Join(root, ".agent", "TOOLS.md"):                "# TOOLS\n\nPrefer read before edit.",
		filepath.Join(root, ".agent", "USER.md"):                 "Preferred language: Chinese",
		filepath.Join(root, ".agent", "memory", "MEMORY.md"):     "User likes concise answers.",
		filepath.Join(root, ".agent", "memory", "2026-03-20.md"): "Yesterday note.",
		filepath.Join(root, ".agent", "memory", "2026-03-21.md"): "Today note.",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}

	prompt, err := ws.BuildSystemPrompt("Base prompt")
	if err != nil {
		t.Fatalf("BuildSystemPrompt returned error: %v", err)
	}

	checks := []string{
		"# Workspace Context",
		"## AGENT.md",
		"Use memory carefully.",
		"## SOUL.md",
		"Be direct.",
		"## TOOLS.md",
		"Prefer read before edit.",
		"## USER.md",
		"Preferred language: Chinese",
		"## .agent/memory/MEMORY.md",
		"User likes concise answers.",
		"## .agent/memory/2026-03-20.md",
		"Yesterday note.",
		"## .agent/memory/2026-03-21.md",
		"Today note.",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("prompt = %q, want substring %q", prompt, check)
		}
	}
}

func TestBuildSystemPromptFallsBackToLegacyWorkspaceLayout(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}

	originalNowFunc := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 3, 21, 10, 0, 0, 0, time.Local) }
	defer func() { nowFunc = originalNowFunc }()

	if err := os.MkdirAll(filepath.Join(root, "workspace", "memory"), 0o755); err != nil {
		t.Fatalf("failed to create legacy workspace memory directory: %v", err)
	}
	files := map[string]string{
		filepath.Join(root, "workspace", "AGENT.md"):                "# AGENT\n\nLegacy agent instructions.",
		filepath.Join(root, "workspace", "SOUL.md"):                 "# SOUL\n\nLegacy soul.",
		filepath.Join(root, "workspace", "TOOLS.md"):                "# TOOLS\n\nLegacy tool guidance.",
		filepath.Join(root, "workspace", "USER.md"):                 "Legacy preferred language: Chinese",
		filepath.Join(root, "workspace", "memory", "MEMORY.md"):     "Legacy long-term memory.",
		filepath.Join(root, "workspace", "memory", "2026-03-20.md"): "Legacy yesterday note.",
		filepath.Join(root, "workspace", "memory", "2026-03-21.md"): "Legacy today note.",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", path, err)
		}
	}

	prompt, err := ws.BuildSystemPrompt("Base prompt")
	if err != nil {
		t.Fatalf("BuildSystemPrompt returned error: %v", err)
	}

	checks := []string{
		"Legacy agent instructions.",
		"Legacy soul.",
		"Legacy tool guidance.",
		"Legacy preferred language: Chinese",
		"## workspace/memory/MEMORY.md",
		"Legacy long-term memory.",
		"## workspace/memory/2026-03-20.md",
		"Legacy yesterday note.",
		"## workspace/memory/2026-03-21.md",
		"Legacy today note.",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("prompt = %q, want substring %q", prompt, check)
		}
	}
}

func TestBuildSystemPromptPrefersDotAgentOverLegacyWorkspace(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}

	if err := os.MkdirAll(filepath.Join(root, ".agent"), 0o755); err != nil {
		t.Fatalf("failed to create .agent directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0o755); err != nil {
		t.Fatalf("failed to create workspace directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "AGENT.md"), []byte("Primary instructions."), 0o644); err != nil {
		t.Fatalf("failed to write .agent AGENT.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "workspace", "AGENT.md"), []byte("Legacy instructions."), 0o644); err != nil {
		t.Fatalf("failed to write workspace AGENT.md: %v", err)
	}

	prompt, err := ws.BuildSystemPrompt("Base prompt")
	if err != nil {
		t.Fatalf("BuildSystemPrompt returned error: %v", err)
	}

	if !strings.Contains(prompt, "Primary instructions.") {
		t.Fatalf("prompt = %q, want primary .agent instructions", prompt)
	}
	if strings.Contains(prompt, "Legacy instructions.") {
		t.Fatalf("prompt = %q, should prefer .agent over legacy workspace", prompt)
	}
}

func TestAppendMemoryPrefersWorkspaceDailyMemoryFile(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	if err := os.MkdirAll(filepath.Join(root, ".agent"), 0o755); err != nil {
		t.Fatalf("failed to create .agent directory: %v", err)
	}

	originalNowFunc := nowFunc
	nowFunc = func() time.Time { return time.Date(2026, 3, 21, 11, 30, 0, 0, time.Local) }
	defer func() { nowFunc = originalNowFunc }()

	if err := ws.AppendMemory("inspect repo", "done"); err != nil {
		t.Fatalf("AppendMemory returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, ".agent", "memory", "2026-03-21.md"))
	if err != nil {
		t.Fatalf("failed to read .agent daily memory file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "**Task:** inspect repo") {
		t.Fatalf("memory content = %q, want task entry", text)
	}
	if fileExists(filepath.Join(root, ".agent", "memory", "MEMORY.md")) {
		t.Fatalf("long-term memory file should not receive automatic daily entries")
	}
	if fileExists(filepath.Join(root, "agent_memory.md")) {
		t.Fatalf("legacy memory file should not be created when .agent/ exists")
	}
}

func TestMemoryEnabledSupportsLegacyWorkspaceMemory(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}

	if ws.MemoryEnabled() {
		t.Fatalf("MemoryEnabled should be false when no memory files exist")
	}

	if err := os.MkdirAll(filepath.Join(root, "workspace", "memory"), 0o755); err != nil {
		t.Fatalf("failed to create legacy workspace memory directory: %v", err)
	}

	if !ws.MemoryEnabled() {
		t.Fatalf("MemoryEnabled should be true when legacy workspace memory directory exists")
	}
}
