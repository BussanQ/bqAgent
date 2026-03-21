package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefinitionsMatchCurrentAgentPyContract(t *testing.T) {
	definitions := Definitions()
	if len(definitions) != 6 {
		t.Fatalf("definitions length = %d, want 6", len(definitions))
	}

	tests := []struct {
		index       int
		name        string
		description string
		required    []string
	}{
		{index: 0, name: "execute_bash", description: "Execute a bash command", required: []string{"command"}},
		{index: 1, name: "read_file", description: "Read a file", required: []string{"path"}},
		{index: 2, name: "write_file", description: "Write to a file", required: []string{"path", "content"}},
		{index: 3, name: "web_search", description: "Search the web for up-to-date information", required: []string{"query"}},
		{index: 4, name: "mem_save", description: "Save knowledge to memory. Use target=\"longterm\" for durable facts, preferences, and patterns. Use target=\"daily\" for session notes and task context.", required: []string{"target", "content"}},
		{index: 5, name: "mem_get", description: "Read memory contents. Use to recall saved knowledge and context.", required: []string{"target"}},
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
	}
}

func TestWriteFileReturnsCurrentSuccessString(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "hello.txt")

	result, err := WriteFile(map[string]any{"path": path, "content": "Hello World"})
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
