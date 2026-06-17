package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefinitionsMatchCurrentAgentPyContract(t *testing.T) {
	definitions := Definitions()
	if len(definitions) != 13 {
		t.Fatalf("definitions length = %d, want 13", len(definitions))
	}

	tests := []struct {
		index       int
		name        string
		description string
		required    []string
	}{
		{index: 0, name: "execute_bash", description: "Execute a bash command", required: []string{"command"}},
		{index: 1, name: "read_file", description: "Read a file. Optionally pass offset (1-based start line) and limit (number of lines) to read part of a large file.", required: []string{"path"}},
		{index: 2, name: "write_file", description: "Write to a file (overwrites the whole file). For changing part of an existing file, prefer edit_file.", required: []string{"path", "content"}},
		{index: 3, name: "edit_file", description: "Replace an exact string in a file. old_string must match exactly once unless replace_all is true. Far more efficient than rewriting the whole file.", required: []string{"path", "old_string", "new_string"}},
		{index: 4, name: "grep", description: "Search file contents by regular expression (Go regexp). Returns path:line:text. Skips .git and binary files.", required: []string{"pattern"}},
		{index: 5, name: "glob", description: "Find files by glob pattern (supports ** for any depth, e.g. **/*.go). Returns paths, most-recently-modified first.", required: []string{"pattern"}},
		{index: 6, name: "todo_write", description: "Create or update the task list for the current work. Pass todos as a JSON array string of {content, status, activeForm}, status in pending|in_progress|completed. Keep one item in_progress at a time.", required: []string{"todos"}},
		{index: 7, name: "web_search", description: "Search the web for up-to-date information via Tavily. Requires SEARCH_API_KEY; Firecrawl env vars are supported as a compatibility fallback.", required: []string{"query"}},
		{index: 8, name: "web_fetch", description: "Fetch content from a web URL", required: []string{"url"}},
		{index: 9, name: "install_skill", description: "Install a workspace skill from a URL into .agent/skills/<name>/SKILL.md.", required: []string{"url"}},
		{index: 10, name: "mem_save", description: "Save knowledge to memory. Use target=\"longterm\" for durable facts, preferences, and patterns. Use target=\"daily\" for session notes and task context.", required: []string{"target", "content"}},
		{index: 11, name: "mem_get", description: "Read memory contents. Use to recall saved knowledge and context.", required: []string{"target"}},
		{index: 12, name: "run_skill", description: "Execute a workspace skill when one of the loaded skills is relevant to the task.", required: []string{"skill"}},
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
	if len(definitions) != 14 {
		t.Fatalf("definitions length = %d, want 14", len(definitions))
	}
	if definitions[len(definitions)-1].Function.Name != "plan" {
		t.Fatalf("last definition name = %q, want %q", definitions[len(definitions)-1].Function.Name, "plan")
	}
	foundRunSkill := false
	foundInstallSkill := false
	for _, definition := range definitions {
		if definition.Function.Name == "run_skill" {
			foundRunSkill = true
		}
		if definition.Function.Name == "install_skill" {
			foundInstallSkill = true
		}
	}
	if !foundRunSkill {
		t.Fatal("definitions missing run_skill")
	}
	if !foundInstallSkill {
		t.Fatal("definitions missing install_skill")
	}
	registry := catalog.Registry()
	if len(registry) != 12 {
		t.Fatalf("registry length = %d, want 12", len(registry))
	}
	if _, ok := registry["install_skill"]; !ok {
		t.Fatal("registry missing install_skill")
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
