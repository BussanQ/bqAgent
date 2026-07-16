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
			ACP: defaultACPConfig(agent, getenv(prefix+"ACP_CMD"), getenv(prefix+"ACP_ARGS")),
			CLI: defaultCLIConfig(agent, getenv(prefix+"CLI_CMD"), getenv(prefix+"CLI_ARGS")),
		}
	}
	return config
}

func SupportedAgents() []AgentName {
	return []AgentName{AgentClaude, AgentCodex, AgentCursor, AgentOpenCode}
}

func defaultACPConfig(agent AgentName, command, args string) CommandSpec {
	command = strings.TrimSpace(command)
	if command == "" && agent == AgentOpenCode {
		command = "opencode"
	}
	if strings.TrimSpace(args) != "" {
		return CommandSpec{Command: command, Args: splitArgs(args)}
	}
	if agent == AgentOpenCode {
		return CommandSpec{Command: command, Args: []string{"acp"}}
	}
	return CommandSpec{Command: command}
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
