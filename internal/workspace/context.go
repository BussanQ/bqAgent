package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const memoryTailLines = 50

var nowFunc = time.Now

func (w *Workspace) BuildSystemPrompt(base string) (string, error) {
	parts := []string{strings.TrimSpace(base), w.workspaceSection()}

	workspaceDocs, err := w.loadWorkspaceDocuments()
	if err != nil {
		return "", err
	}
	if workspaceDocs != "" {
		parts = append(parts, workspaceDocs)
	}

	rules, err := w.loadRules()
	if err != nil {
		return "", err
	}
	if rules != "" {
		parts = append(parts, rules)
	}

	skills, err := w.loadSkillsSummary()
	if err != nil {
		return "", err
	}
	if skills != "" {
		parts = append(parts, skills)
	}

	memory, err := w.loadMemoryContext(memoryTailLines)
	if err != nil {
		return "", err
	}
	if memory != "" {
		parts = append(parts, "# Memory\n"+memory)
	}

	return strings.Join(nonEmpty(parts), "\n\n"), nil
}

func (w *Workspace) AppendMemory(task, result string) error {
	task = strings.TrimSpace(task)
	result = strings.TrimSpace(result)
	if task == "" && result == "" {
		return nil
	}

	now := nowFunc()
	memoryPath := w.MemoryPath()
	if w.UsesWorkspaceContext() {
		memoryPath = w.DailyMemoryPath(now.Format("2006-01-02"))
	}
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		return err
	}

	entry := fmt.Sprintf("\n## %s\n**Task:** %s\n**Result:** %s\n", now.Format("2006-01-02 15:04:05"), task, result)
	file, err := os.OpenFile(memoryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(entry)
	return err
}

func (w *Workspace) workspaceSection() string {
	lines := []string{
		"# Workspace",
		"Root: " + w.Root,
		"Context directory: .agent/{AGENT.md, SOUL.md, TOOLS.md, USER.md}",
		"Workspace long-term memory: .agent/memory/MEMORY.md",
		"Workspace daily memory: .agent/memory/YYYY-MM-DD.md (loads today and yesterday; new session notes append to today)",
		"Legacy memory file: agent_memory.md",
		"Rules directory: .agent/rules/*.md",
		"Skills directory: .agent/skills/*/SKILL.md",
		"Sessions directory: .agent/sessions/",
		"MCP config: .agent/mcp.json (definition only; live MCP is not enabled yet)",
	}
	return strings.Join(lines, "\n")
}

func (w *Workspace) loadWorkspaceDocuments() (string, error) {
	documents := []struct {
		label string
		path  string
	}{
		{label: "AGENT.md", path: w.WorkspaceAgentPath()},
		{label: "SOUL.md", path: w.WorkspaceSoulPath()},
		{label: "TOOLS.md", path: w.WorkspaceToolsPath()},
		{label: "USER.md", path: w.WorkspaceUserPath()},
	}

	blocks := make([]string, 0, len(documents))
	for _, document := range documents {
		content, err := os.ReadFile(document.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(string(content))
		if text == "" {
			continue
		}
		blocks = append(blocks, "## "+document.label+"\n"+text)
	}

	if len(blocks) == 0 {
		return "", nil
	}
	return "# Workspace Context\n\n" + strings.Join(blocks, "\n\n"), nil
}

func (w *Workspace) loadRules() (string, error) {
	entries, err := os.ReadDir(w.RulesDir())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	blocks := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}

		content, err := os.ReadFile(filepath.Join(w.RulesDir(), entry.Name()))
		if err != nil {
			return "", err
		}
		blocks = append(blocks, "## "+strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))+"\n"+strings.TrimSpace(string(content)))
	}

	if len(blocks) == 0 {
		return "", nil
	}
	return "# Rules\n\n" + strings.Join(blocks, "\n\n"), nil
}

func (w *Workspace) loadSkillsSummary() (string, error) {
	entries, err := os.ReadDir(w.SkillsDir())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	lines := []string{"# Skills"}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(w.SkillsDir(), entry.Name(), "SKILL.md")
		content, err := os.ReadFile(skillPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}

		title, summary := summarizeSkill(entry.Name(), string(content))
		lines = append(lines, fmt.Sprintf("- %s: %s", title, summary))
	}

	if len(lines) == 1 {
		return "", nil
	}
	return strings.Join(lines, "\n"), nil
}

func summarizeSkill(fallbackName, content string) (string, string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for index := 1; index < len(lines); index++ {
			if strings.TrimSpace(lines[index]) == "---" {
				start = index + 1
				break
			}
		}
	}

	title := fallbackName
	paragraph := make([]string, 0)
	for _, rawLine := range lines[start:] {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			if title == fallbackName {
				title = strings.TrimSpace(strings.TrimLeft(line, "#"))
			}
			continue
		}
		paragraph = append(paragraph, line)
	}

	summary := "Markdown skill definition available."
	if len(paragraph) > 0 {
		summary = strings.Join(paragraph, " ")
	}
	return title, summary
}

func (w *Workspace) loadMemoryContext(maxLines int) (string, error) {
	blocks := make([]string, 0, 4)

	workspaceMemory, err := readTail(w.WorkspaceMemoryPath(), maxLines)
	if err != nil {
		return "", err
	}
	if workspaceMemory != "" {
		blocks = append(blocks, "## .agent/memory/MEMORY.md\n"+workspaceMemory)
	}

	now := nowFunc()
	dailyFiles := []string{
		now.AddDate(0, 0, -1).Format("2006-01-02"),
		now.Format("2006-01-02"),
	}
	for _, day := range dailyFiles {
		dailyMemory, err := readTail(w.DailyMemoryPath(day), maxLines)
		if err != nil {
			return "", err
		}
		if dailyMemory == "" {
			continue
		}
		blocks = append(blocks, "## .agent/memory/"+day+".md\n"+dailyMemory)
	}

	legacyMemory, err := readTail(w.LegacyMemoryPath(), maxLines)
	if err != nil {
		return "", err
	}
	if legacyMemory != "" && w.LegacyMemoryPath() != w.WorkspaceMemoryPath() {
		blocks = append(blocks, "## agent_memory.md\n"+legacyMemory)
	}

	if len(blocks) == 0 {
		return "", nil
	}
	return strings.Join(blocks, "\n\n"), nil
}

func readTail(path string, maxLines int) (string, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(string(content), "\n"), "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

func nonEmpty(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
