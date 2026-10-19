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

const previousDefaultAgentDoc = `# AGENT.md - Operating Instructions

Personal defaults for every workspace. Repository instructions take precedence.

## Tools
- The tools exposed in the current request are the source of truth for availability, capabilities, and parameters.
- Locate code with glob and grep, then read the relevant parts with read_file. If a search finds nothing, change the pattern or the tool instead of repeating the call.
- File and command output may be truncated. Narrow the read or command when needed; omitted output is not absent content.
- Keep shell commands safe and reversible, and prefer the file tools over the shell for reading and editing files.

## Progress tracking
- task_update is the persistent record of a multi-step programming task: its goal, acceptance criteria and status.
- todo_write is an optional working checklist for genuinely long tasks. Never use it to restate the request or to mirror task_update, and never let it delay the real work.

## Skills
- The skill list contains discovery metadata, not full instructions. Before using a relevant skill, read its complete SKILL.md with read_file.
- An explicit /skill selection follows the same read_file workflow; it does not execute a separate run_skill tool.

## Verification
- For anything with a runtime (a page, a UI, a service), running it is the proof: serve it, load it and drive the real interaction. "It's in the code", "it loads" and "no syntax error" do not mean it works.
- Grepping for a name, counting braces or citing a byte count proves nothing. Run the real thing, or mark it unverified.
- An install that fails once is unavailable here. Do not hunt for alternatives or retry variations; say so, verify another way and mark what you could not prove as unverified.
- Design, prose, mockups and research have no test to pass: produce them well and describe them briefly.

## Response style
- Break complex explanations into steps, and show code changes as diffs when helpful.
`

func TestDefaultAgentGuidanceUsesSingleTaskUpdateAndMigratesPreviousDefault(t *testing.T) {
	data, err := defaultFiles.ReadFile("defaults/AGENT.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "todo_write") {
		t.Fatal("default AGENT.md must not mention todo_write")
	}
	if strings.Contains(string(data), "task_update") {
		t.Fatal("default AGENT.md must not mention task_update; the tool is opt-in via ENABLE_TASK_TOOL")
	}
	ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	if err := os.MkdirAll(ws.AgentDir(), 0755); err != nil {
		t.Fatal(err)
	}
	custom := "custom guidance mentioning todo_write"
	if err := os.WriteFile(filepath.Join(ws.AgentDir(), "AGENT.md"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ws.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(ws.AgentDir(), "AGENT.md"))
	if string(got) != custom {
		t.Fatalf("custom AGENT.md changed: %q", got)
	}

	upgraded := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	if err := os.MkdirAll(upgraded.AgentDir(), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(upgraded.AgentDir(), "AGENT.md")
	if err := os.WriteFile(path, []byte(previousDefaultAgentDoc), 0644); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != string(data) {
		t.Fatalf("previous default AGENT.md was not upgraded: %q", got)
	}
}

const taskUpdateDefaultAgentDoc = `# AGENT.md - Operating Instructions

Personal defaults for every workspace. Repository instructions take precedence.

## Tools
- The tools exposed in the current request are the source of truth for availability, capabilities, and parameters.
- Locate code with glob and grep, then read the relevant parts with read_file. If a search finds nothing, change the pattern or the tool instead of repeating the call.
- File and command output may be truncated. Narrow the read or command when needed; omitted output is not absent content.
- Keep shell commands safe and reversible, and prefer the file tools over the shell for reading and editing files.

## Progress tracking
- task_update is the single persistent record for a multi-step programming task: goal, acceptance criteria, remaining work and status.
- Do not create a task for ordinary Q&A or a simple single-step change.
- For multi-step implementation, register the goal, acceptance criteria and remaining work before editing.
- For read-only analysis, review or report work set kind=analysis: it completes when each criterion cites concrete reading, search or report evidence and nothing remains; do not run tests just to close it. Tasks that change files keep the default kind=code and need real verification.
- Update it at phase changes, when acceptance evidence is obtained, when blocked, or when scope changes. Do not repeat the plan after every tool call.

## Skills
- The skill list contains discovery metadata, not full instructions. Before using a relevant skill, read its complete SKILL.md with read_file.
- An explicit /skill selection follows the same read_file workflow; it does not execute a separate run_skill tool.

## Verification
- For anything with a runtime (a page, a UI, a service), running it is the proof: serve it, load it and drive the real interaction. "It's in the code", "it loads" and "no syntax error" do not mean it works.
- Grepping for a name, counting braces or citing a byte count proves nothing. Run the real thing, or mark it unverified.
- An install that fails once is unavailable here. Do not hunt for alternatives or retry variations; say so, verify another way and mark what you could not prove as unverified.
- Design, prose, mockups and research have no test to pass: produce them well and describe them briefly.

## Response style
- Break complex explanations into steps, and show code changes as diffs when helpful.
`

func TestEnsureDefaultsMigratesTaskUpdateDefaultButKeepsCustomizedCopy(t *testing.T) {
	data, err := defaultFiles.ReadFile("defaults/AGENT.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"default": taskUpdateDefaultAgentDoc, "edited": taskUpdateDefaultAgentDoc + "\nmine\n"} {
		ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
		if err := os.MkdirAll(ws.AgentDir(), 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(ws.AgentDir(), "AGENT.md")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := ws.EnsureDefaults(); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		want := content
		if name == "default" {
			want = string(data)
		}
		if string(got) != want {
			t.Fatalf("%s AGENT.md migrated incorrectly: %q", name, got)
		}
	}
}
