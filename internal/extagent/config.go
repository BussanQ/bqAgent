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
		if agent == AgentCodex {
			cfg := config.Agents[agent]
			command := strings.TrimSpace(getenv(prefix + "APP_SERVER_CMD"))
			rawArgs := strings.TrimSpace(getenv(prefix + "APP_SERVER_ARGS"))
			if command != "" || rawArgs != "" {
				if command == "" {
					command = "codex"
				}
				args := []string{"app-server"}
				if rawArgs != "" {
					args = splitArgs(rawArgs)
				}
				cfg.AppServer = CommandSpec{Command: command, Args: args}
			} else if strings.TrimSpace(getenv(prefix+"ACP_CMD")) == "" &&
				strings.TrimSpace(getenv(prefix+"ACP_ARGS")) == "" &&
				strings.TrimSpace(getenv(prefix+"CLI_CMD")) == "" &&
				strings.TrimSpace(getenv(prefix+"CLI_ARGS")) == "" {
				cfg.AppServer = CommandSpec{Command: "codex", Args: []string{"app-server", "proxy"}}
				cfg.AutoAppServer = true
			}
			config.Agents[agent] = cfg
		}
	}
	return config
}

func SupportedAgents() []AgentName {
	return []AgentName{AgentClaude, AgentCodex, AgentCursor, AgentOpenCode, AgentPi}
}

func defaultACPConfig(agent AgentName, command, args string) CommandSpec {
	command = strings.TrimSpace(command)
	if command == "" {
		switch agent {
		case AgentCursor:
			command = "cursor-agent"
		case AgentOpenCode:
			command = "opencode"
		}
	}
	if strings.TrimSpace(args) != "" {
		return CommandSpec{Command: command, Args: splitArgs(args)}
	}
	switch agent {
	case AgentCursor:
		return CommandSpec{Command: command, Args: []string{"acp"}}
	case AgentOpenCode:
		return CommandSpec{Command: command, Args: []string{"acp"}}
	default:
		return CommandSpec{Command: command}
	}
}

func defaultCLIConfig(agent AgentName, command, args string) CommandSpec {
	command = strings.TrimSpace(command)
	if command == "" {
		switch agent {
		case AgentClaude:
			command = "claude"
		case AgentCodex:
			command = "codex"
		case AgentPi:
			command = "pi"
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
	case AgentPi:
		return CommandSpec{Command: command, Args: []string{"--mode", "json"}}
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
