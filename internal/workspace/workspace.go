package workspace

import (
	"os"
	"path/filepath"
	"strings"
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
	Root           string
	GlobalAgentDir string
}

// New returns a workspace whose primary agent configuration lives in
// ~/.agent. Tests and compatibility callers that construct Workspace literals
// without GlobalAgentDir continue to use <workspace>/.agent as a self-contained
// configuration root.
func New(root string) (*Workspace, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Workspace{Root: filepath.Clean(root), GlobalAgentDir: filepath.Join(home, agentDirName)}, nil
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

	globalAgentDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		globalAgentDir = filepath.Clean(filepath.Join(home, agentDirName))
	}
	for {
		localAgentDir := filepath.Clean(filepath.Join(root, agentDirName))
		hasLocalAgentMarker := localAgentDir != globalAgentDir && fileExists(localAgentDir)
		if hasLocalAgentMarker || fileExists(filepath.Join(root, ".git")) || fileExists(filepath.Join(root, "go.mod")) {
			return New(root)
		}

		parent := filepath.Dir(root)
		if parent == root {
			return New(filepath.Clean(start))
		}
		root = parent
	}
}

func (w *Workspace) AgentDir() string {
	if w != nil && strings.TrimSpace(w.GlobalAgentDir) != "" {
		return filepath.Clean(w.GlobalAgentDir)
	}
	return filepath.Join(w.Root, agentDirName)
}

func (w *Workspace) LocalAgentDir() string {
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

func (w *Workspace) LocalMemoryDir() string {
	return filepath.Join(w.LocalAgentDir(), memoryDirName)
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

func (w *Workspace) LocalMCPConfigPath() string {
	return filepath.Join(w.LocalAgentDir(), mcpConfigFileName)
}

func (w *Workspace) MCPConfigPaths() []string {
	paths := []string{w.MCPConfigPath()}
	if local := w.LocalMCPConfigPath(); filepath.Clean(local) != filepath.Clean(paths[0]) {
		paths = append(paths, local)
	}
	return paths
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

func (w *Workspace) HasLocalAgentConfig() bool {
	return fileExists(w.LocalAgentDir())
}

func (w *Workspace) primaryConfigLayer() *Workspace {
	if w == nil || strings.TrimSpace(w.GlobalAgentDir) == "" {
		return w
	}
	agentDir := filepath.Clean(w.GlobalAgentDir)
	return &Workspace{Root: filepath.Dir(agentDir)}
}

func (w *Workspace) hasDistinctLocalConfig() bool {
	return w.hasDistinctConfigRoot() && w.HasLocalAgentConfig()
}

func (w *Workspace) hasDistinctConfigRoot() bool {
	return w != nil && strings.TrimSpace(w.GlobalAgentDir) != "" && filepath.Clean(w.AgentDir()) != filepath.Clean(w.LocalAgentDir())
}

func (w *Workspace) hasLegacyContext() bool {
	return fileExists(w.LegacyContextDir())
}
