package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillsDiscoversDirectoryLinksAndSkipsInvalidLinks(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "global"
		if local {
			name = "workspace"
		}
		t.Run(name, func(t *testing.T) {
			ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
			skills := filepath.Join(ws.AgentDir(), "skills")
			if local {
				skills = filepath.Join(ws.Root, ".agent", "skills")
			}
			if err := os.MkdirAll(skills, 0755); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\ndescription: Linked workflow.\nalias: explain\n---\nprivate body"), 0644); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, target, filepath.Join(skills, "absolute"))
			relative, err := filepath.Rel(skills, target)
			if err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, relative, filepath.Join(skills, "relative"))
			symlinkOrSkip(t, "absolute", filepath.Join(skills, "chain"))
			symlinkOrSkip(t, "missing", filepath.Join(skills, "dangling"))
			symlinkOrSkip(t, "cycle", filepath.Join(skills, "cycle"))
			symlinkOrSkip(t, filepath.Join(target, "SKILL.md"), filepath.Join(skills, "file"))
			symlinkOrSkip(t, t.TempDir(), filepath.Join(skills, "empty"))
			found, err := ws.LoadSkills()
			if err != nil {
				t.Fatal(err)
			}
			if len(found) != 3 {
				t.Fatalf("skills = %+v", found)
			}
			for i, id := range []string{"absolute", "chain", "relative"} {
				skill := found[i]
				if skill.ID != id || skill.Description != "Linked workflow." || len(skill.Aliases) != 1 || skill.Aliases[0] != "explain" || skill.Path != ".agent/skills/"+id+"/SKILL.md" {
					t.Fatalf("skill = %+v", skill)
				}
			}
			prompt, err := ws.BuildSystemPrompt("base")
			if err != nil || !strings.Contains(prompt, "name: absolute") || strings.Contains(prompt, "private body") {
				t.Fatalf("prompt=%q err=%v", prompt, err)
			}
		})
	}
}

func TestLinkedWorkspaceSkillOverridesGlobalAndResolvesAlias(t *testing.T) {
	ws := &Workspace{Root: t.TempDir(), GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	global := filepath.Join(ws.AgentDir(), "skills", "demo")
	local := filepath.Join(ws.Root, ".agent", "skills")
	for _, dir := range []string{global, local} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(global, "SKILL.md"), []byte("---\ndescription: Global.\nalias: old\n---"), 0644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\ndescription: Local link.\nalias: new\n---"), 0644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, target, filepath.Join(local, "demo"))
	skill, handled, err := ws.ResolveSkill("new")
	if err != nil || !handled || skill.ID != "demo" || skill.Description != "Local link." {
		t.Fatalf("skill=%+v handled=%v err=%v", skill, handled, err)
	}
	if _, handled, err := ws.ResolveSkill("old"); err != nil || handled {
		t.Fatalf("old alias survived: %v %v", handled, err)
	}
}
