package extagent

import "strings"

func ConfigFromEnv(getenv func(string) string, workspaceRoot string) Config {
	config := Config{
		WorkspaceRoot: workspaceRoot,
		Agents:        map[AgentName]AgentConfig{},
	}
	for _, agent := range SupportedAgents() {
		prefix := "AGENT_" + strings.ToUpper(string(agent)) + "_"
		config.Agents[agent] = AgentConfig{
			ACP: CommandSpec{
				Command: strings.TrimSpace(getenv(prefix + "ACP_CMD")),
				Args:    splitArgs(getenv(prefix + "ACP_ARGS")),
			},
			CLI: defaultCLIConfig(agent, getenv(prefix+"CLI_CMD"), getenv(prefix+"CLI_ARGS")),
		}
	}
	return config
}

func SupportedAgents() []AgentName {
	return []AgentName{AgentClaude, AgentCodex, AgentCursor, AgentOpenCode}
}

func defaultCLIConfig(agent AgentName, command, args string) CommandSpec {
	command = strings.TrimSpace(command)
	if command == "" {
		switch agent {
		case AgentClaude:
			command = "claude"
		case AgentCodex:
			command = "codex"
		}
	}
	if strings.TrimSpace(args) != "" {
		return CommandSpec{Command: command, Args: splitArgs(args)}
	}
	switch agent {
	case AgentClaude:
		return CommandSpec{Command: command, Args: []string{"-p", "--output-format", "json"}}
	case AgentCodex:
		return CommandSpec{Command: command, Args: []string{"exec", "--json", "--skip-git-repo-check"}}
	default:
		return CommandSpec{Command: command}
	}
}

func splitArgs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Fields(raw)
}
