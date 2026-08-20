package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultSkillDescription  = "No description provided."
	maxSkillMetadataBytes    = 16 * 1024
	maxSkillDescriptionBytes = 1024
)

// Skill describes discovery metadata for .agent/skills/*/SKILL.md.
type Skill struct {
	ID          string
	Description string
	Path        string
	Aliases     []string
}

const memoryTailLines = 50

var nowFunc = time.Now

func (w *Workspace) BuildSystemPrompt(base string) (string, error) {
	primary := w.primaryConfigLayer()
	root, err := primary.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()

	parts := []string{strings.TrimSpace(base), w.workspaceSection()}

	workspaceDocs, err := primary.loadWorkspaceDocuments(root)
	if err != nil {
		return "", err
	}
	if workspaceDocs != "" {
		if w.hasDistinctConfigRoot() {
			workspaceDocs = "# Global Context\n\n" + strings.TrimPrefix(workspaceDocs, "# Workspace Context\n\n")
		}
		parts = append(parts, workspaceDocs)
	}

	rules, err := primary.loadRules(root)
	if err != nil {
		return "", err
	}
	if rules != "" {
		parts = append(parts, rules)
	}

	skills, err := primary.loadSkillsSection(root)
	if err != nil {
		return "", err
	}
	if skills != "" {
		parts = append(parts, skills)
	}

	memory, err := primary.loadMemoryContext(root, memoryTailLines)
	if err != nil {
		return "", err
	}
	if memory != "" {
		parts = append(parts, "# Memory\n"+memory)
	}

	if w.hasDistinctLocalConfig() {
		local := &Workspace{Root: w.Root}
		localRoot, openErr := local.openRoot()
		if openErr != nil {
			return "", openErr
		}
		localDocs, loadErr := local.loadWorkspaceDocuments(localRoot)
		localMemory := ""
		if loadErr == nil {
			localMemory, loadErr = local.loadMemoryContext(localRoot, memoryTailLines)
		}
		closeErr := localRoot.Close()
		if loadErr != nil {
			return "", loadErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if localDocs != "" {
			parts = append(parts, "# Workspace Context\n\n"+strings.TrimPrefix(localDocs, "# Workspace Context\n\n"))
		}
		if localMemory != "" {
			parts = append(parts, "# Workspace Memory\n"+localMemory)
		}
	}

	return strings.Join(nonEmpty(parts), "\n\n"), nil
}

func (w *Workspace) AppendMemory(task, result string) error {
	if primary := w.primaryConfigLayer(); primary.Root != w.Root {
		return primary.AppendMemory(task, result)
	}
	task = strings.TrimSpace(task)
	result = strings.TrimSpace(result)
	if task == "" && result == "" {
		return nil
	}

	root, err := w.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	now := nowFunc()
	memoryPath := w.MemoryPath()
	if w.UsesWorkspaceContext() {
		memoryPath = w.DailyMemoryPath(now.Format("2006-01-02"))
	}
	memoryPath, err = w.pathInsideRoot(memoryPath)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		return err
	}

	entry := fmt.Sprintf("\n## %s\n**Task:** %s\n**Result:** %s\n", now.Format("2006-01-02 15:04:05"), task, result)
	file, err := root.OpenFile(memoryPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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
		"Global agent directory: " + w.AgentDir(),
		"Primary context: ~/.agent/{AGENT.md, SOUL.md, TOOLS.md, USER.md}",
		"Optional workspace context: .agent/{AGENT.md, SOUL.md, USER.md} (merged after global context)",
		"Legacy compatible context directory: workspace/{AGENT.md, SOUL.md, TOOLS.md, USER.md}",
		"Global long-term memory: ~/.agent/memory/MEMORY.md",
		"Global daily memory: ~/.agent/memory/YYYY-MM-DD.md (loads today and yesterday; new session notes append to today)",
		"Legacy compatible memory directory: workspace/memory/{MEMORY.md, YYYY-MM-DD.md}",
		"Legacy memory file: agent_memory.md",
		"Global rules directory: ~/.agent/rules/*.md",
		"Global skills directory: ~/.agent/skills/*/SKILL.md",
		"Sessions directory: .agent/sessions/",
		"MCP config: ~/.agent/mcp.json merged with optional workspace .agent/mcp.json",
	}
	return strings.Join(lines, "\n")
}

func (w *Workspace) loadWorkspaceDocuments(root *os.Root) (string, error) {
	documents := []struct {
		label string
		paths []string
	}{
		{label: "AGENT.md", paths: []string{w.WorkspaceAgentPath(), filepath.Join(w.LegacyContextDir(), agentDocFileName)}},
		{label: "SOUL.md", paths: []string{w.WorkspaceSoulPath(), filepath.Join(w.LegacyContextDir(), soulDocFileName)}},
		{label: "TOOLS.md", paths: []string{w.WorkspaceToolsPath(), filepath.Join(w.LegacyContextDir(), toolsDocFileName)}},
		{label: "USER.md", paths: []string{w.WorkspaceUserPath(), filepath.Join(w.LegacyContextDir(), userDocFileName)}},
	}

	blocks := make([]string, 0, len(documents))
	for _, document := range documents {
		content, err := w.readFirstAvailable(root, document.paths...)
		if err != nil {
			return "", err
		}
		if len(content) == 0 {
			continue
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

func (w *Workspace) loadRules(root *os.Root) (string, error) {
	entries, err := w.readDirFromRoot(root, w.RulesDir())
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

		content, err := w.readFileFromRoot(root, filepath.Join(w.RulesDir(), entry.Name()))
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

func (w *Workspace) LoadSkills() ([]Skill, error) {
	primary := w.primaryConfigLayer()
	root, err := primary.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return primary.loadSkills(root)
}

func (w *Workspace) loadSkills(root *os.Root) ([]Skill, error) {
	entries, err := w.readDirFromRoot(root, w.SkillsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillPath := filepath.Join(w.SkillsDir(), entry.Name(), "SKILL.md")
		description, aliases, exists, err := w.readSkillMetadata(root, skillPath)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		promptPath, err := w.skillPromptPath(skillPath)
		if err != nil {
			return nil, err
		}
		skills = append(skills, Skill{
			ID:          entry.Name(),
			Description: description,
			Path:        promptPath,
			Aliases:     aliases,
		})
	}
	return skills, nil
}

func (w *Workspace) LoadSkill(id string) (Skill, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Skill{}, fmt.Errorf("skill id is required")
	}
	skills, err := w.LoadSkills()
	if err != nil {
		return Skill{}, err
	}
	for _, skill := range skills {
		if skill.ID == id {
			return skill, nil
		}
	}
	return Skill{}, fmt.Errorf("skill %q not found", id)
}

func (w *Workspace) ResolveSkill(token string) (Skill, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Skill{}, false, nil
	}
	skills, err := w.LoadSkills()
	if err != nil {
		return Skill{}, false, err
	}
	for _, skill := range skills {
		if strings.EqualFold(skill.ID, token) {
			return skill, true, nil
		}
	}

	matches := make([]Skill, 0, 1)
	for _, skill := range skills {
		for _, alias := range skill.Aliases {
			if strings.EqualFold(alias, token) {
				matches = append(matches, skill)
				break
			}
		}
	}
	if len(matches) == 0 {
		return Skill{}, false, nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, skill := range matches {
			ids = append(ids, skill.ID)
		}
		return Skill{}, true, fmt.Errorf("skill alias %q is ambiguous: %s", token, strings.Join(ids, ", "))
	}
	return matches[0], true, nil
}

func (w *Workspace) loadSkillsSection(root *os.Root) (string, error) {
	skills, err := w.loadSkills(root)
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "", nil
	}

	lines := []string{
		"# Skills",
		"The following entries are discovery metadata only. When a user request clearly matches a listed skill, first use read_file to read the complete SKILL.md at the listed path. Then follow that file's instructions. Do not infer the workflow from this metadata alone.",
	}
	for _, skill := range skills {
		lines = append(lines,
			"- name: "+skill.ID,
			"  description: "+skill.Description,
			"  path: "+skill.Path,
		)
	}
	return strings.Join(lines, "\n"), nil
}

func (w *Workspace) skillPromptPath(skillPath string) (string, error) {
	relative, err := filepath.Rel(w.Root, skillPath)
	if err != nil {
		return "", err
	}
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("skill path %q escapes workspace root", skillPath)
	}
	return filepath.ToSlash(relative), nil
}

func (w *Workspace) readSkillMetadata(root *os.Root, path string) (description string, aliases []string, exists bool, err error) {
	path, err = w.pathInsideRoot(path)
	if err != nil {
		return "", nil, false, err
	}
	file, err := root.Open(path)
	if os.IsNotExist(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxSkillMetadataBytes))
	if err != nil {
		return "", nil, false, err
	}
	description, aliases = parseSkillMetadata(string(content))
	return description, aliases, true, nil
}

func parseSkillMetadata(content string) (string, []string) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return defaultSkillDescription, nil
	}

	description := ""
	aliases := make([]string, 0)
	seen := make(map[string]bool)
	inAliasesList := false
	closed := false
	for index := 1; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if line == "---" {
			closed = true
			break
		}
		if line == "" {
			continue
		}

		if inAliasesList {
			if strings.HasPrefix(line, "-") {
				aliases = appendUniqueAlias(aliases, seen, strings.TrimSpace(strings.TrimPrefix(line, "-")))
				continue
			}
			inAliasesList = false
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "description":
			description = cleanSkillDescription(value)
		case "alias":
			aliases = appendUniqueAlias(aliases, seen, value)
		case "aliases":
			if value == "" {
				inAliasesList = true
				continue
			}
			for _, alias := range strings.Split(value, ",") {
				aliases = appendUniqueAlias(aliases, seen, alias)
			}
		}
	}
	if !closed {
		return defaultSkillDescription, nil
	}
	if description == "" {
		description = defaultSkillDescription
	}
	return description, aliases
}

func cleanSkillDescription(raw string) string {
	description := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
	if len(description) <= maxSkillDescriptionBytes {
		return description
	}
	var builder strings.Builder
	for _, character := range description {
		if builder.Len()+len(string(character)) > maxSkillDescriptionBytes {
			break
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func appendUniqueAlias(aliases []string, seen map[string]bool, raw string) []string {
	alias := cleanSkillAlias(raw)
	if alias == "" {
		return aliases
	}
	key := strings.ToLower(alias)
	if seen[key] {
		return aliases
	}
	seen[key] = true
	return append(aliases, alias)
}

func cleanSkillAlias(raw string) string {
	alias := strings.TrimSpace(raw)
	alias = strings.Trim(alias, `"'`)
	return strings.TrimSpace(alias)
}

func (w *Workspace) loadMemoryContext(root *os.Root, maxLines int) (string, error) {
	entriesPath, err := w.pathInsideRoot(filepath.Join(w.WorkspaceMemoryDir(), "entries.jsonl"))
	if err != nil {
		return "", err
	}
	if _, err := root.Stat(entriesPath); err == nil {
		return "", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	blocks := make([]string, 0, 4)

	workspaceMemory, workspaceMemoryPath, err := w.readPreferredTail(root, maxLines, w.WorkspaceMemoryPath(), w.LegacyWorkspaceMemoryPath())
	if err != nil {
		return "", err
	}
	if workspaceMemory != "" {
		blocks = append(blocks, "## "+w.displayPath(workspaceMemoryPath)+"\n"+workspaceMemory)
	}

	now := nowFunc()
	dailyFiles := []string{
		now.AddDate(0, 0, -1).Format("2006-01-02"),
		now.Format("2006-01-02"),
	}
	for _, day := range dailyFiles {
		dailyMemory, dailyMemoryPath, err := w.readPreferredTail(root, maxLines, w.DailyMemoryPath(day), w.LegacyDailyMemoryPath(day))
		if err != nil {
			return "", err
		}
		if dailyMemory == "" {
			continue
		}
		blocks = append(blocks, "## "+w.displayPath(dailyMemoryPath)+"\n"+dailyMemory)
	}

	legacyMemory, err := w.readTail(root, w.LegacyMemoryPath(), maxLines)
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

func (w *Workspace) readTail(root *os.Root, path string, maxLines int) (string, error) {
	content, err := w.readFileFromRoot(root, path)
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

func (w *Workspace) readFirstAvailable(root *os.Root, paths ...string) ([]byte, error) {
	for _, path := range paths {
		content, err := w.readFileFromRoot(root, path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return content, nil
	}
	return nil, nil
}

func (w *Workspace) readPreferredTail(root *os.Root, maxLines int, paths ...string) (string, string, error) {
	for _, path := range paths {
		content, err := w.readTail(root, path, maxLines)
		if err != nil {
			return "", "", err
		}
		if content == "" {
			continue
		}
		return content, path, nil
	}
	return "", "", nil
}

func (w *Workspace) displayPath(path string) string {
	relative, err := filepath.Rel(w.Root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
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
