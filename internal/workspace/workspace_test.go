package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func TestDefaultToolsDocumentMatchesToolDefinitions(t *testing.T) {
	content, err := defaultFiles.ReadFile("defaults/TOOLS.md")
	if err != nil {
		t.Fatal(err)
	}

	documented := make(map[string]bool)
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- **") {
			continue
		}
		name, _, ok := strings.Cut(strings.TrimPrefix(line, "- **"), "**:")
		if ok {
			documented[name] = true
		}
	}

	definitions := tools.Definitions()
	definitions = append(definitions, tools.PlanDefinition(), tools.StructuredMemoryDefinition())
	for _, definition := range definitions {
		name := definition.Function.Name
		if !documented[name] {
			t.Errorf("default TOOLS.md does not document %q", name)
		}
		delete(documented, name)
	}
	for name := range documented {
		t.Errorf("default TOOLS.md documents unknown tool %q", name)
	}
}

func TestDiscoverFindsNearestWorkspaceMarker(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	if ws.AgentDir() != filepath.Join(home, ".agent") {
		t.Fatalf("agent dir = %q, want global home config", ws.AgentDir())
	}
	if ws.GlobalMemoryDir() != filepath.Join(home, ".agent", "memory") {
		t.Fatalf("global memory dir = %q, want ~/.agent/memory", ws.GlobalMemoryDir())
	}
	if ws.WorkspaceMemoryDir() == ws.GlobalMemoryDir() {
		t.Fatal("workspace memory dir should be distinct from global memory dir")
	}
}

func TestDiscoverDoesNotTreatGlobalAgentDirectoryAsWorkspaceMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	start := filepath.Join(home, "scratch", "nested")
	if err := os.MkdirAll(filepath.Join(home, ".agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := Discover(start)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root != start {
		t.Fatalf("workspace root = %q, want starting directory %q", ws.Root, start)
	}
}

func TestBuildSystemPromptMergesGlobalAndLocalContext(t *testing.T) {
	root := t.TempDir()
	globalAgent := filepath.Join(t.TempDir(), ".agent")
	for _, dir := range []string{globalAgent, filepath.Join(root, ".agent"), filepath.Join(globalAgent, "memory"), filepath.Join(root, ".agent", "memory")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(globalAgent, "AGENT.md"):               "global instructions",
		filepath.Join(globalAgent, "USER.md"):                "global user",
		filepath.Join(globalAgent, "memory", "MEMORY.md"):    "global memory",
		filepath.Join(root, ".agent", "AGENT.md"):            "workspace instructions",
		filepath.Join(root, ".agent", "USER.md"):             "workspace user",
		filepath.Join(root, ".agent", "memory", "MEMORY.md"): "workspace memory",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	prompt, err := (&Workspace{Root: root, GlobalAgentDir: globalAgent}).BuildSystemPrompt("base")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Global Context", "global instructions", "global user", "# Workspace Context", "workspace instructions", "workspace user", "global memory", "workspace memory"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Index(prompt, "global instructions") > strings.Index(prompt, "workspace instructions") {
		t.Fatalf("workspace context must load after global context: %s", prompt)
	}
}

func TestLoadSkillsMergesGlobalAndWorkspaceWithLocalOverride(t *testing.T) {
	root := t.TempDir()
	globalAgent := filepath.Join(t.TempDir(), ".agent")
	files := map[string]string{
		filepath.Join(globalAgent, "skills", "global-only", "SKILL.md"):   "---\ndescription: Global only.\nalias: collision\n---\n# Global",
		filepath.Join(globalAgent, "skills", "shared", "SKILL.md"):        "---\ndescription: Global shared.\nalias: old-alias\n---\n# Global shared",
		filepath.Join(root, ".agent", "skills", "local-only", "SKILL.md"): "---\ndescription: Local only.\nalias: collision\n---\n# Local",
		filepath.Join(root, ".agent", "skills", "SHARED", "SKILL.md"):     "---\ndescription: Local shared.\nalias: new-alias\n---\n# Local shared",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "global-only"), 0o755); err != nil {
		t.Fatal(err)
	}

	ws := &Workspace{Root: root, GlobalAgentDir: globalAgent}
	skills, err := ws.LoadSkills()
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 3 || skills[0].ID != "global-only" || skills[1].ID != "local-only" || skills[2].ID != "SHARED" {
		t.Fatalf("merged skills = %#v", skills)
	}
	shared, handled, err := ws.ResolveSkill("shared")
	if err != nil || !handled || shared.Description != "Local shared." || len(shared.Aliases) != 1 || shared.Aliases[0] != "new-alias" {
		t.Fatalf("resolved shared = (%#v, %t, %v)", shared, handled, err)
	}
	loadedShared, err := ws.LoadSkill("shared")
	if err != nil || loadedShared.ID != "SHARED" {
		t.Fatalf("LoadSkill shared = %#v, err = %v", loadedShared, err)
	}
	if _, handled, err := ws.ResolveSkill("old-alias"); err != nil || handled {
		t.Fatalf("old overridden alias = handled %t, err %v", handled, err)
	}
	if resolved, handled, err := ws.ResolveSkill("new-alias"); err != nil || !handled || resolved.ID != "SHARED" {
		t.Fatalf("workspace alias = (%#v, %t, %v)", resolved, handled, err)
	}
	if _, handled, err := ws.ResolveSkill("collision"); !handled || err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("merged alias collision = handled %t, err %v", handled, err)
	}
	prompt, err := ws.BuildSystemPrompt("base")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"name: global-only", "name: local-only", "name: SHARED", "description: Local shared."} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "Global shared.") {
		t.Fatalf("prompt retained overridden global skill: %s", prompt)
	}
}

func TestEnsureLocalConfigCreatesOnlyWorkspaceOverlayFiles(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root, GlobalAgentDir: filepath.Join(t.TempDir(), ".agent")}
	if err := ws.EnsureLocalConfig(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"memory", "mcp.json", "AGENT.md", "SOUL.md", "USER.md"} {
		if _, err := os.Stat(filepath.Join(root, ".agent", path)); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}
	for _, path := range []string{"TOOLS.md", filepath.Join("memory", "MEMORY.md"), "rules", "skills"} {
		if _, err := os.Stat(filepath.Join(root, ".agent", path)); !os.IsNotExist(err) {
			t.Fatalf("unexpected workspace overlay path %s: %v", path, err)
		}
	}
}

func TestEnsureDefaultsInitializesGlobalAgentDirectoryOnly(t *testing.T) {
	root := t.TempDir()
	globalAgent := filepath.Join(t.TempDir(), ".agent")
	ws := &Workspace{Root: root, GlobalAgentDir: globalAgent}
	if err := ws.EnsureDefaults(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENT.md", "SOUL.md", "TOOLS.md", "USER.md", "mcp.json", filepath.Join("memory", "MEMORY.md")} {
		if _, err := os.Stat(filepath.Join(globalAgent, path)); err != nil {
			t.Fatalf("global defaults missing %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".agent")); !os.IsNotExist(err) {
		t.Fatalf("global initialization unexpectedly wrote workspace .agent: %v", err)
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
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("---\ndescription: Helps summarize repository changes.\nalias: hidden-alias\n---\n\n# Demo Skill\n\nFull instructions stay out of the system prompt."), 0o644); err != nil {
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
		"The following entries are discovery metadata only.",
		"- name: demo",
		"description: Helps summarize repository changes.",
		"path: .agent/skills/demo/SKILL.md",
		"# Memory",
		"## agent_memory.md",
		"recent memory",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Fatalf("prompt = %q, want substring %q", prompt, check)
		}
	}
	for _, forbidden := range []string{"hidden-alias", "Full instructions stay out of the system prompt.", filepath.Join(root, ".agent", "skills")} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt = %q, must not contain %q", prompt, forbidden)
		}
	}
}

func TestBuildPromptSectionsKeepsStableHashIndependentFromMemory(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	rulePath := filepath.Join(root, ".agent", "rules", "cache.md")
	memoryPath := filepath.Join(root, "agent_memory.md")
	if err := os.WriteFile(rulePath, []byte("Keep static instructions stable.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath, []byte("first memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := ws.BuildPromptSections("Base\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := ws.BuildPromptSections("Base\n")
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated {
		t.Fatalf("repeated sections differ:\nfirst=%#v\nsecond=%#v", first, repeated)
	}
	if strings.Contains(first.Stable, "first memory") || !strings.Contains(first.SessionContext, "first memory") {
		t.Fatalf("sections = %#v, want memory only in session context", first)
	}

	if err := os.WriteFile(memoryPath, []byte("second memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterMemory, err := ws.BuildPromptSections("Base\n")
	if err != nil {
		t.Fatal(err)
	}
	if afterMemory.Stable != first.Stable || afterMemory.StableHash != first.StableHash {
		t.Fatalf("memory changed stable section: before=%#v after=%#v", first, afterMemory)
	}
	if afterMemory.SessionContext == first.SessionContext {
		t.Fatal("memory change did not change session context")
	}

	if err := os.WriteFile(rulePath, []byte("Changed static rule.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterRule, err := ws.BuildPromptSections("Base\n")
	if err != nil {
		t.Fatal(err)
	}
	if afterRule.StableHash == first.StableHash {
		t.Fatal("rule change did not change stable hash")
	}
}

func TestLoadSkillReturnsStructuredSkill(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skills directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("---\ndescription: Helps summarize repository changes.\n---\n\n# Demo Skill\n\nPrivate workflow body."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	skill, err := ws.LoadSkill("demo")
	if err != nil {
		t.Fatalf("LoadSkill returned error: %v", err)
	}
	if skill.ID != "demo" {
		t.Fatalf("skill.ID = %q, want %q", skill.ID, "demo")
	}
	if skill.Description != "Helps summarize repository changes." {
		t.Fatalf("skill.Description = %q, want description", skill.Description)
	}
	if skill.Path != ".agent/skills/demo/SKILL.md" {
		t.Fatalf("skill.Path = %q, want workspace-relative path", skill.Path)
	}
}

func TestLoadSkillParsesAliasesFromFrontMatter(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skills directory: %v", err)
	}
	content := "---\ndescription: Helps summarize repository changes.\naliases:\n  - aihot\n  - ai日报\nalias: hot\n---\n\n# Demo Skill\n\nPrivate workflow body."
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	skill, err := ws.LoadSkill("demo")
	if err != nil {
		t.Fatalf("LoadSkill returned error: %v", err)
	}
	if skill.Description != "Helps summarize repository changes." {
		t.Fatalf("skill.Description = %q, want description", skill.Description)
	}
	wantAliases := []string{"aihot", "ai日报", "hot"}
	if len(skill.Aliases) != len(wantAliases) {
		t.Fatalf("skill.Aliases = %v, want %v", skill.Aliases, wantAliases)
	}
	for index, want := range wantAliases {
		if skill.Aliases[index] != want {
			t.Fatalf("skill.Aliases[%d] = %q, want %q", index, skill.Aliases[index], want)
		}
	}
}

func TestLoadSkillWithoutFrontMatterUsesDefaultDescription(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	dir := filepath.Join(root, ".agent", "skills", "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create skills directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Secret workflow\n\nDo not expose this body during discovery."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	skill, err := ws.LoadSkill("plain")
	if err != nil {
		t.Fatalf("LoadSkill returned error: %v", err)
	}
	if skill.Description != defaultSkillDescription {
		t.Fatalf("skill.Description = %q, want %q", skill.Description, defaultSkillDescription)
	}
	if len(skill.Aliases) != 0 {
		t.Fatalf("skill.Aliases = %v, want none", skill.Aliases)
	}
}

func TestResolveSkillMatchesIDAndAlias(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	if err := os.MkdirAll(filepath.Join(root, ".agent", "skills", "demo"), 0o755); err != nil {
		t.Fatalf("failed to create skills directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent", "skills", "demo", "SKILL.md"), []byte("---\naliases: aihot, ai日报\n---\n\n# Demo Skill\n\nHelps summarize repository changes."), 0o644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	byID, handled, err := ws.ResolveSkill("demo")
	if err != nil || !handled || byID.ID != "demo" {
		t.Fatalf("ResolveSkill by id = (%+v, %t, %v), want demo handled", byID, handled, err)
	}
	byAlias, handled, err := ws.ResolveSkill("aihot")
	if err != nil || !handled || byAlias.ID != "demo" {
		t.Fatalf("ResolveSkill by alias = (%+v, %t, %v), want demo handled", byAlias, handled, err)
	}
	_, handled, err = ws.ResolveSkill("missing")
	if err != nil || handled {
		t.Fatalf("ResolveSkill missing = handled %t err %v, want unhandled nil", handled, err)
	}
}

func TestResolveSkillReturnsAmbiguousAliasError(t *testing.T) {
	root := t.TempDir()
	ws := &Workspace{Root: root}
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(root, ".agent", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create skills directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nalias: aihot\n---\n\n# "+name+"\n\nSummary."), 0o644); err != nil {
			t.Fatalf("failed to write skill file: %v", err)
		}
	}

	_, handled, err := ws.ResolveSkill("aihot")
	if !handled || err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ResolveSkill ambiguous = handled %t err %v, want ambiguity", handled, err)
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
