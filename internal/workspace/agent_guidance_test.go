package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultAgentGuidanceLoadsWithoutToolsDocument(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "global"
		if local {
			name = "workspace"
		}
		t.Run(name, func(t *testing.T) {
			ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
			if err := ws.EnsureDefaults(); err != nil {
				t.Fatal(err)
			}
			directory := ws.AgentDir()
			if local {
				if err := ws.EnsureLocalConfig(); err != nil {
					t.Fatal(err)
				}
				directory = ws.LocalAgentDir()
			}
			if _, err := os.Stat(filepath.Join(directory, "TOOLS.md")); !os.IsNotExist(err) {
				t.Fatalf("unexpected TOOLS.md: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(directory, "AGENT.md"))
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := ws.BuildSystemPrompt("")
			if err != nil {
				t.Fatal(err)
			}
			for _, guidance := range []string{
				"The tools exposed in the current request are the source of truth",
				"Before using a relevant skill, read its complete SKILL.md with read_file.",
				"task_update is the persistent record",
			} {
				if !strings.Contains(string(content), guidance) || !strings.Contains(prompt, guidance) {
					t.Fatalf("AGENT.md guidance missing from initialized file or prompt: %q", guidance)
				}
			}
		})
	}
}

func TestBuildSystemPromptIgnoresRetiredToolsDocuments(t *testing.T) {
	ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	directories := []string{ws.AgentDir(), ws.LocalAgentDir(), ws.LegacyContextDir(), filepath.Join(filepath.Dir(ws.AgentDir()), "workspace")}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "TOOLS.md"), []byte("retired tool instructions"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	const custom = "Custom agent guidance must survive initialization."
	if err := os.WriteFile(filepath.Join(ws.AgentDir(), "AGENT.md"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ws.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	prompt, err := ws.BuildSystemPrompt("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, custom) || strings.Contains(prompt, "retired tool instructions") || strings.Contains(prompt, "TOOLS.md") {
		t.Fatalf("unexpected context: %s", prompt)
	}
	content, err := os.ReadFile(filepath.Join(ws.AgentDir(), "AGENT.md"))
	if err != nil || string(content) != custom {
		t.Fatalf("existing AGENT.md changed: %q, %v", content, err)
	}
	for _, directory := range directories {
		content, err := os.ReadFile(filepath.Join(directory, "TOOLS.md"))
		if err != nil || string(content) != "retired tool instructions" {
			t.Fatalf("legacy user file modified: %s: %q, %v", directory, content, err)
		}
	}
}
