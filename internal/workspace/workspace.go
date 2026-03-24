package workspace

import (
	"os"
	"path/filepath"
)

const (
	legacyMemoryFileName = "agent_memory.md"
	agentDirName         = ".agent"
	legacyContextDirName = "workspace"
	rulesDirName         = "rules"
	skillsDirName        = "skills"
	mcpConfigFileName    = "mcp.json"
	sessionsDirName      = "sessions"

	contextDirName    = ".agent"
	agentDocFileName  = "AGENT.md"
	soulDocFileName   = "SOUL.md"
	toolsDocFileName  = "TOOLS.md"
	userDocFileName   = "USER.md"
	memoryDirName     = "memory"
	memoryDocFileName = "MEMORY.md"
)

type Workspace struct {
	Root string
}

func Discover(start string) (*Workspace, error) {
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		start = cwd
	}

	root := filepath.Clean(start)
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		root = filepath.Dir(root)
	}

	for {
		if fileExists(filepath.Join(root, agentDirName)) || fileExists(filepath.Join(root, ".git")) || fileExists(filepath.Join(root, "go.mod")) {
			return &Workspace{Root: root}, nil
		}

		parent := filepath.Dir(root)
		if parent == root {
			return &Workspace{Root: filepath.Clean(start)}, nil
		}
		root = parent
	}
}

func (w *Workspace) AgentDir() string {
	return filepath.Join(w.Root, agentDirName)
}

func (w *Workspace) LegacyContextDir() string {
	return filepath.Join(w.Root, legacyContextDirName)
}

func (w *Workspace) ContextDir() string {
	return filepath.Join(w.Root, contextDirName)
}

func (w *Workspace) WorkspaceAgentPath() string {
	return filepath.Join(w.ContextDir(), agentDocFileName)
}

func (w *Workspace) WorkspaceSoulPath() string {
	return filepath.Join(w.ContextDir(), soulDocFileName)
}

func (w *Workspace) WorkspaceToolsPath() string {
	return filepath.Join(w.ContextDir(), toolsDocFileName)
}

func (w *Workspace) WorkspaceUserPath() string {
	return filepath.Join(w.ContextDir(), userDocFileName)
}

func (w *Workspace) WorkspaceMemoryDir() string {
	return filepath.Join(w.ContextDir(), memoryDirName)
}

func (w *Workspace) LegacyWorkspaceMemoryDir() string {
	return filepath.Join(w.LegacyContextDir(), memoryDirName)
}

func (w *Workspace) WorkspaceMemoryPath() string {
	return filepath.Join(w.WorkspaceMemoryDir(), memoryDocFileName)
}

func (w *Workspace) LegacyWorkspaceMemoryPath() string {
	return filepath.Join(w.LegacyWorkspaceMemoryDir(), memoryDocFileName)
}

func (w *Workspace) DailyMemoryPath(day string) string {
	return filepath.Join(w.WorkspaceMemoryDir(), day+".md")
}

func (w *Workspace) LegacyDailyMemoryPath(day string) string {
	return filepath.Join(w.LegacyWorkspaceMemoryDir(), day+".md")
}

func (w *Workspace) LegacyMemoryPath() string {
	return filepath.Join(w.Root, legacyMemoryFileName)
}

func (w *Workspace) MemoryPath() string {
	if w.UsesWorkspaceContext() {
		return w.WorkspaceMemoryPath()
	}
	return w.LegacyMemoryPath()
}

func (w *Workspace) RulesDir() string {
	return filepath.Join(w.AgentDir(), rulesDirName)
}

func (w *Workspace) SkillsDir() string {
	return filepath.Join(w.AgentDir(), skillsDirName)
}

func (w *Workspace) SessionsDir() string {
	return filepath.Join(w.AgentDir(), sessionsDirName)
}

func (w *Workspace) MCPConfigPath() string {
	return filepath.Join(w.AgentDir(), mcpConfigFileName)
}

func (w *Workspace) ResolvePath(path string) string {
	if w == nil || path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(w.Root, path)
}

func (w *Workspace) UsesWorkspaceContext() bool {
	return w.hasPrimaryContext() || w.hasLegacyContext()
}

func (w *Workspace) MemoryEnabled() bool {
	return fileExists(w.LegacyMemoryPath()) ||
		fileExists(w.WorkspaceMemoryPath()) ||
		fileExists(w.LegacyWorkspaceMemoryPath()) ||
		fileExists(w.WorkspaceMemoryDir()) ||
		fileExists(w.LegacyWorkspaceMemoryDir())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (w *Workspace) hasPrimaryContext() bool {
	return fileExists(w.ContextDir())
}

func (w *Workspace) hasLegacyContext() bool {
	return fileExists(w.LegacyContextDir())
}
