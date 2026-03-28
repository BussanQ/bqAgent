package extagent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func Detect(ctx context.Context, config Config, factory ACPClientFactory) map[AgentName]DetectionResult {
	if factory == nil {
		factory = NewACPClient
	}
	results := make(map[AgentName]DetectionResult, len(config.Agents))
	for _, agent := range SupportedAgents() {
		cfg := config.Agents[agent]
		result := DetectionResult{Agent: agent}

		if looksExecutable(cfg.CLI.Command) {
			result.CLI = &AgentTransport{Agent: agent, Kind: TransportCLI, Command: cfg.CLI}
		}

		if looksExecutable(cfg.ACP.Command) {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			client, err := factory(cfg.ACP, config.WorkspaceRoot)
			if err == nil {
				err = client.Initialize(probeCtx)
				_ = client.Close()
			}
			cancel()
			if err == nil {
				result.ACP = &AgentTransport{Agent: agent, Kind: TransportACP, Command: cfg.ACP}
			} else {
				result.StartupError = err.Error()
			}
		}

		switch {
		case result.ACP != nil:
			result.Preferred = result.ACP
		case result.CLI != nil:
			result.Preferred = result.CLI
			result.CLIFallback = result.StartupError != ""
		}
		results[agent] = result
	}
	return results
}

func looksExecutable(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if strings.Contains(command, `\`) || strings.Contains(command, `/`) {
		return true
	}
	_, err := exec.LookPath(command)
	return err == nil
}

func FormatStatuses(results map[AgentName]DetectionResult) []string {
	lines := make([]string, 0, len(results))
	for _, agent := range SupportedAgents() {
		result := results[agent]
		status := "unavailable"
		if result.Preferred != nil {
			status = string(result.Preferred.Kind)
			if result.CLIFallback {
				status = status + "-fallback"
			}
		}
		lines = append(lines, fmt.Sprintf("%s=%s", agent, status))
	}
	return lines
}
