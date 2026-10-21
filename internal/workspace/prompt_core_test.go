package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubPromptEnvironment(t *testing.T, goos, shell string) {
	t.Helper()
	originalGOOS, originalGOARCH, originalGetenv, originalNow := runtimeGOOS, runtimeGOARCH, getenv, nowFunc
	runtimeGOOS, runtimeGOARCH = goos, "arm64"
	getenv = func(key string) string {
		if key == "SHELL" {
			return shell
		}
		return ""
	}
	nowFunc = func() time.Time { return time.Date(2026, 3, 21, 10, 0, 0, 0, time.Local) }
	t.Cleanup(func() {
		runtimeGOOS, runtimeGOARCH, getenv, nowFunc = originalGOOS, originalGOARCH, originalGetenv, originalNow
	})
}

func writeTestFiles(t *testing.T, files map[string]string) {
	t.Helper()
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildPromptSectionsLayersCoreRepositoryAndEnvironment(t *testing.T) {
	stubPromptEnvironment(t, "darwin", "/bin/zsh")
	root := t.TempDir()
	writeTestFiles(t, map[string]string{
		filepath.Join(root, "AGENTS.md"):    "# Project\n<!-- maintainer note -->\nOnly run targeted tests.",
		filepath.Join(root, "CLAUDE.md"):    "Ignored when AGENTS.md exists.",
		filepath.Join(root, ".git", "HEAD"): "ref: refs/heads/main",
	})
	ws := &Workspace{Root: root, GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}

	sections, err := ws.BuildPromptSections("")
	if err != nil {
		t.Fatal(err)
	}
	stable := sections.Stable
	if !strings.HasPrefix(stable, "# Core Instructions") {
		t.Fatalf("stable prompt must start with core instructions: %q", stable)
	}
	for _, want := range []string{
		"# Repository Instructions\nFrom AGENTS.md at the workspace root.",
		"Only run targeted tests.",
		"# Environment\nOS: macOS (darwin/arm64)",
		"bash runs commands with sh -c (POSIX shell); the user's login shell is zsh. BSD userland",
		"bash_wait or bash_kill with the job_id; jobs do not survive the current turn.",
		"Git repository: yes",
		"# Memory Policy",
	} {
		if !strings.Contains(stable, want) {
			t.Fatalf("stable prompt missing %q: %s", want, stable)
		}
	}
	for _, unwanted := range []string{"maintainer note", "Ignored when AGENTS.md exists.", "Legacy", "2026-03-21"} {
		if strings.Contains(stable, unwanted) {
			t.Fatalf("stable prompt must not contain %q: %s", unwanted, stable)
		}
	}
	if core, repo, env := strings.Index(stable, "# Core Instructions"), strings.Index(stable, "# Repository Instructions"), strings.Index(stable, "# Environment"); !(core < repo && repo < env) {
		t.Fatalf("unexpected section order: core=%d repo=%d env=%d", core, repo, env)
	}
	if !strings.Contains(sections.SessionContext, "Session started: 2026-03-21 (Saturday)") {
		t.Fatalf("session context missing start date: %q", sections.SessionContext)
	}

	writeTestFiles(t, map[string]string{
		filepath.Join(root, "agent_memory.md"):        "note",
		filepath.Join(root, "workspace", "USER.md"):   "legacy user",
		filepath.Join(ws.AgentDir(), "rules", "a.md"): "rule",
	})
	withLegacy, err := ws.BuildPromptSections("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Legacy context directory: workspace/", "Legacy memory file: agent_memory.md"} {
		if !strings.Contains(withLegacy.Stable, want) {
			t.Fatalf("existing legacy layout should be listed, missing %q", want)
		}
	}
}

func TestRepositoryInstructionsFallBackToClaudeAndTruncate(t *testing.T) {
	stubPromptEnvironment(t, "windows", "")
	root := t.TempDir()
	writeTestFiles(t, map[string]string{filepath.Join(root, "CLAUDE.md"): "Use pnpm."})
	ws := &Workspace{Root: root}
	got, err := ws.loadRepositoryInstructions()
	if err != nil || !strings.Contains(got, "From CLAUDE.md") || !strings.Contains(got, "Use pnpm.") {
		t.Fatalf("CLAUDE.md fallback = %q, %v", got, err)
	}

	writeTestFiles(t, map[string]string{filepath.Join(root, "AGENTS.md"): strings.Repeat("规则\n", maxRepositoryInstructionBytes)})
	got, err = ws.loadRepositoryInstructions()
	if err != nil || !strings.Contains(got, "[AGENTS.md truncated; read the rest with read_file.]") || len(got) > maxRepositoryInstructionBytes+512 {
		t.Fatalf("oversized AGENTS.md: len=%d err=%v", len(got), err)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("truncation must not split a UTF-8 sequence")
	}

	environment := ws.environmentSection()
	for _, want := range []string{"OS: Windows (windows/arm64)", "cmd /C", "Git repository: no"} {
		if !strings.Contains(environment, want) {
			t.Fatalf("environment missing %q: %s", want, environment)
		}
	}
}

func TestCleanContextDocumentSkipsCommentOnlyTemplates(t *testing.T) {
	template, err := defaultFiles.ReadFile("defaults/USER.md")
	if err != nil {
		t.Fatal(err)
	}
	if got := cleanContextDocument(string(template)); got != "" {
		t.Fatalf("comment-only USER.md template = %q, want empty", got)
	}
	if got := cleanContextDocument("# USER\r\n<!-- hint\nmore -->\n\n\n\nPrefers Chinese.  \n"); got != "# USER\n\nPrefers Chinese." {
		t.Fatalf("cleaned document = %q", got)
	}

	ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	if err := ws.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	prompt, err := ws.BuildSystemPrompt("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "## USER.md") || !strings.Contains(prompt, "## SOUL.md") {
		t.Fatalf("default USER.md template should be omitted: %s", prompt)
	}
}

func TestEnsureDefaultsUpgradesOnlyUneditedRetiredDefaults(t *testing.T) {
	const retired = "# AGENT.md - retired default\n"
	sum := sha256.Sum256([]byte(retired))
	original := retiredDefaults[agentDocFileName]
	retiredDefaults[agentDocFileName] = append(append([]string(nil), original...), hex.EncodeToString(sum[:]))
	t.Cleanup(func() { retiredDefaults[agentDocFileName] = original })

	current, err := defaultFiles.ReadFile("defaults/AGENT.md")
	if err != nil {
		t.Fatal(err)
	}
	ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	const custom = "# AGENT.md - retired default\nMy own rule.\n"
	writeTestFiles(t, map[string]string{
		filepath.Join(ws.AgentDir(), "AGENT.md"):      retired,
		filepath.Join(ws.AgentDir(), "SOUL.md"):       custom,
		filepath.Join(ws.LocalAgentDir(), "AGENT.md"): retired,
	})
	if err := ws.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	if err := ws.EnsureLocalConfig(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(ws.AgentDir(), "AGENT.md"):      string(current),
		filepath.Join(ws.LocalAgentDir(), "AGENT.md"): string(current),
		filepath.Join(ws.AgentDir(), "SOUL.md"):       custom,
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestBuildPromptSectionsSharesUserPrefixAcrossWorkspaces(t *testing.T) {
	stubPromptEnvironment(t, "linux", "/bin/bash")
	globalAgent := filepath.Join(t.TempDir(), ".agent")
	first, second := t.TempDir(), t.TempDir()
	writeTestFiles(t, map[string]string{
		filepath.Join(globalAgent, "AGENT.md"):                     "Global agent rule.",
		filepath.Join(globalAgent, "rules", "style.md"):            "Global style rule.",
		filepath.Join(first, "AGENTS.md"):                          "First project rule.",
		filepath.Join(second, "AGENTS.md"):                         "Second project rule.",
		filepath.Join(second, ".agent", "skills", "x", "SKILL.md"): "---\ndescription: Local skill.\n---\n",
	})
	build := func(root string) string {
		t.Helper()
		sections, err := (&Workspace{Root: root, GlobalAgentDir: globalAgent}).BuildPromptSections("")
		if err != nil {
			t.Fatal(err)
		}
		return sections.Stable
	}
	firstStable, secondStable := build(first), build(second)

	order := []string{"# Core Instructions", "# Memory Policy", "# Global Context", "Global style rule.", "# Repository Instructions"}
	for _, stable := range []string{firstStable, secondStable} {
		previous := -1
		for _, marker := range order {
			index := strings.Index(stable, marker)
			if index <= previous {
				t.Fatalf("%q out of order (index %d after %d): %s", marker, index, previous, stable)
			}
			previous = index
		}
	}
	shared := strings.Index(firstStable, "# Repository Instructions")
	if firstStable[:shared] != secondStable[:shared] {
		t.Fatalf("user-level prefix differs across workspaces:\n%q\n%q", firstStable[:shared], secondStable[:shared])
	}
}
