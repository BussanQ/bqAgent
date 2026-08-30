package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appmemory "bqagent/internal/memory"
)

func TestDefinitionsMatchCurrentAgentPyContract(t *testing.T) {
	definitions := Definitions()
	if len(definitions) != 11 {
		t.Fatalf("definitions length = %d, want 11", len(definitions))
	}

	tests := []struct {
		index       int
		name        string
		description string
		required    []string
	}{
		{index: 0, name: "execute_bash", description: "Execute a bash command when it verifies or produces information needed for the task. Captured combined stdout/stderr is byte-capped and ends with a truncation marker when over budget.", required: []string{"command"}},
		{index: 1, name: "read_file", description: "Read a workspace file using a workspace-relative path, preferably copied from glob output. Absolute paths are accepted only when they are inside the workspace. Optionally pass offset and limit. Returned content is byte-capped and ends with a truncation marker when over budget.", required: []string{"path"}},
		{index: 2, name: "write_file", description: "Write a workspace file using a workspace-relative path (overwrites the whole file). Absolute paths outside the workspace are rejected. Prefer edit_file for partial changes.", required: []string{"path", "content"}},
		{index: 3, name: "edit_file", description: "Replace an exact string in a workspace file using a workspace-relative path. old_string must match exactly once unless replace_all is true.", required: []string{"path", "old_string", "new_string"}},
		{index: 4, name: "grep", description: "Search workspace file contents by Go regular expression. Use a workspace-relative path when provided. Returns path:line:text and skips .git and binary files.", required: []string{"pattern"}},
		{index: 5, name: "glob", description: "Find workspace files by a relative glob pattern (supports ** and brace alternatives, e.g. **/*.{go,md}). Returns workspace-relative paths; reuse those paths in file tools. If no files match, change the pattern or use another exploration tool instead of repeating the same call.", required: []string{"pattern"}},
		{index: 6, name: "todo_write", description: "Optional checklist for genuinely long, multi-step work. It is not part of the standard workflow: for ordinary tasks, do not call todo_write and start substantive work directly. Use it only when you independently decide persistent progress tracking is useful; never call it merely to restate the request, announce a plan, or before routine code analysis. Pass todos as a JSON array string of {content, status, activeForm}, status in pending|in_progress|completed, with at most one item in_progress. After using it, immediately continue with another substantive tool and do not call it again until task content or status changes.", required: []string{"todos"}},
		{index: 7, name: "web_search", description: "Search the web for up-to-date information via Tavily. Requires SEARCH_API_KEY; Firecrawl env vars are supported as a compatibility fallback.", required: []string{"query"}},
		{index: 8, name: "web_fetch", description: "Fetch content from a web URL", required: []string{"url"}},
		{index: 9, name: "install_skill", description: "Install a skill from a URL. Defaults to the global .agent/skills directory; set target=workspace for the current workspace.", required: []string{"url"}},
		{index: 10, name: "mem_save", description: "Save knowledge to memory. Use target=\"longterm\" for durable facts, preferences, and patterns. Use target=\"daily\" for session notes and task context.", required: []string{"target", "content"}},
	}

	for _, testCase := range tests {
		definition := definitions[testCase.index]
		if definition.Type != "function" {
			t.Fatalf("definition[%d].Type = %q, want %q", testCase.index, definition.Type, "function")
		}
		if definition.Function.Name != testCase.name {
			t.Fatalf("definition[%d].Function.Name = %q, want %q", testCase.index, definition.Function.Name, testCase.name)
		}
		if definition.Function.Description != testCase.description {
			t.Fatalf("definition[%d].Function.Description = %q, want %q", testCase.index, definition.Function.Description, testCase.description)
		}
		if len(definition.Function.Parameters.Required) != len(testCase.required) {
			t.Fatalf("definition[%d].required length = %d, want %d", testCase.index, len(definition.Function.Parameters.Required), len(testCase.required))
		}
		for requiredIndex, required := range testCase.required {
			if definition.Function.Parameters.Required[requiredIndex] != required {
				t.Fatalf("definition[%d].required[%d] = %q, want %q", testCase.index, requiredIndex, definition.Function.Parameters.Required[requiredIndex], required)
			}
		}
		if definition.Function.Name == "web_fetch" {
			if _, ok := definition.Function.Parameters.Properties["extract_mode"]; !ok {
				t.Fatal("web_fetch definition missing extract_mode property")
			}
			if _, ok := definition.Function.Parameters.Properties["max_chars"]; !ok {
				t.Fatal("web_fetch definition missing max_chars property")
			}
		}
		if definition.Function.Name == "install_skill" {
			if _, ok := definition.Function.Parameters.Properties["name"]; !ok {
				t.Fatal("install_skill definition missing name property")
			}
			if _, ok := definition.Function.Parameters.Properties["overwrite"]; !ok {
				t.Fatal("install_skill definition missing overwrite property")
			}
			if _, ok := definition.Function.Parameters.Properties["target"]; !ok {
				t.Fatal("install_skill definition missing target property")
			}
		}
	}
}

func TestWriteFileReturnsCurrentSuccessString(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "hello.txt")

	result, err := WriteFile(context.Background(), map[string]any{"path": path, "content": "Hello World"})
	if err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if result != "Wrote to "+path {
		t.Fatalf("WriteFile returned %q, want %q", result, "Wrote to "+path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file back: %v", err)
	}
	if string(content) != "Hello World" {
		t.Fatalf("file content = %q, want %q", string(content), "Hello World")
	}
}

func TestNewCatalogIncludesLocalToolsForServerLikeUsage(t *testing.T) {
	catalog := NewCatalog(Options{IncludePlan: true})
	definitions := catalog.Definitions()
	if len(definitions) != 12 {
		t.Fatalf("definitions length = %d, want 12", len(definitions))
	}
	if definitions[len(definitions)-1].Function.Name != "plan" {
		t.Fatalf("last definition name = %q, want %q", definitions[len(definitions)-1].Function.Name, "plan")
	}
	foundInstallSkill := false
	for _, definition := range definitions {
		if definition.Function.Name == "run_skill" {
			t.Fatal("definitions should not include run_skill")
		}
		if definition.Function.Name == "install_skill" {
			foundInstallSkill = true
		}
	}
	if !foundInstallSkill {
		t.Fatal("definitions missing install_skill")
	}
	registry := catalog.Registry()
	if len(registry) != 11 {
		t.Fatalf("registry length = %d, want 11", len(registry))
	}
	if _, ok := registry["mem_get"]; ok {
		t.Fatal("registry should not include mem_get")
	}
	if _, ok := registry["install_skill"]; !ok {
		t.Fatal("registry missing install_skill")
	}
}

func TestNewCatalogExposesMemorySearchWithoutMemGet(t *testing.T) {
	catalog := NewCatalog(Options{MemoryStore: appmemory.NewStore(t.TempDir())})
	if _, ok := catalog.Registry()["mem_get"]; ok {
		t.Fatal("registry should not include mem_get")
	}
	if _, ok := catalog.Registry()["memory"]; !ok {
		t.Fatal("registry missing memory")
	}
	for _, definition := range catalog.Definitions() {
		if definition.Function.Name == "mem_get" {
			t.Fatal("definitions should not include mem_get")
		}
	}
}

func TestCatalogInjectsToolOutputLimits(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(Options{WorkspaceRoot: root, BashOutputMaxBytes: 3, ReadFileMaxBytes: 3})
	registry := catalog.Registry()

	content, err := registry["read_file"](context.Background(), map[string]any{"path": "large.txt"})
	if err != nil {
		t.Fatalf("catalog read_file error: %v", err)
	}
	if want := "abc" + truncatedOutputMarker; content != want {
		t.Fatalf("catalog read_file = %q, want %q", content, want)
	}

	command := "printf abcdef"
	if runtime.GOOS == "windows" {
		command = "set /p unused=abcdef<nul&exit /b 0"
	}
	output, err := registry["execute_bash"](context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatalf("catalog execute_bash error: %v", err)
	}
	if want := "abc" + truncatedOutputMarker; output != want {
		t.Fatalf("catalog execute_bash = %q, want %q", output, want)
	}
}

func TestCatalogReadsDotAgentPathsFromGlobalAgentDirectory(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	agentDir := filepath.Join(home, ".agent")
	skillDir := filepath.Join(agentDir, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("global skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog := NewCatalog(Options{WorkspaceRoot: workspaceRoot, AgentDir: agentDir})
	content, err := catalog.Registry()["read_file"](context.Background(), map[string]any{"path": ".agent/skills/demo/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	if content != "global skill" {
		t.Fatalf("read_file = %q, want global skill", content)
	}
}

func TestCatalogReadsWorkspaceSkillsBeforeGlobalSkills(t *testing.T) {
	workspaceRoot := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), ".agent")
	for path, content := range map[string]string{
		filepath.Join(agentDir, "skills", "shared", "SKILL.md"):                "global shared",
		filepath.Join(agentDir, "skills", "global-only", "SKILL.md"):           "global only",
		filepath.Join(workspaceRoot, ".agent", "skills", "shared", "SKILL.md"): "workspace shared",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	readFile := NewCatalog(Options{WorkspaceRoot: workspaceRoot, AgentDir: agentDir}).Registry()["read_file"]
	shared, err := readFile(context.Background(), map[string]any{"path": ".agent/skills/shared/SKILL.md"})
	if err != nil || shared != "workspace shared" {
		t.Fatalf("shared skill = %q, err = %v", shared, err)
	}
	globalOnly, err := readFile(context.Background(), map[string]any{"path": ".agent/skills/global-only/SKILL.md"})
	if err != nil || globalOnly != "global only" {
		t.Fatalf("global-only skill = %q, err = %v", globalOnly, err)
	}
}

func TestExecuteBashReturnsErrorAndOutputOnNonZeroExit(t *testing.T) {
	command := "printf sentinel; exit 7"
	if runtime.GOOS == "windows" {
		command = "echo sentinel && exit /b 7"
	}
	output, err := ExecuteBash(context.Background(), map[string]any{"command": command})
	if err == nil {
		t.Fatal("expected non-zero exit error")
	}
	if !strings.Contains(output, "sentinel") {
		t.Fatalf("output = %q, want sentinel", output)
	}
}

func TestExecuteBashSharesBoundedOutputBudget(t *testing.T) {
	command := "printf 123456; printf abcdef >&2"
	if runtime.GOOS == "windows" {
		command = "set /p unused=123456<nul&set /p unused=abcdef<nul 1>&2&exit /b 0"
	}

	output, err := ExecuteBashInDirWithMaxOutput("", 8)(context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatalf("ExecuteBash error: %v", err)
	}
	if want := "123456ab" + truncatedOutputMarker; output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestExecuteBashHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ExecuteBash(ctx, map[string]any{"command": "ping 127.0.0.1"})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}
