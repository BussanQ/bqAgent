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
		if agent == AgentCodex && looksExecutable(cfg.AppServer.Command) {
			spec, err := detectCodexAppServer(ctx, cfg, config.WorkspaceRoot, probeCodexAppServer, startCodexDaemon)
			if err == nil {
				result.AppServer = &AgentTransport{Agent: agent, Kind: TransportAppServer, Command: spec}
			} else {
				result.StartupError = "app-server: " + err.Error()
			}
		}

		if cliTransportSupported(agent) && looksExecutable(cfg.CLI.Command) {
			result.CLI = &AgentTransport{Agent: agent, Kind: TransportCLI, Command: cfg.CLI}
		}

		if looksExecutable(cfg.ACP.Command) {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			client, err := factory(probeCtx, cfg.ACP, config.WorkspaceRoot)
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
		case result.AppServer != nil:
			result.Preferred = result.AppServer
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

// Resolve the transport before any thread or turn is started. Fallbacks must
// never replay a user task. Keep the successful command for subsequent clients.
func detectCodexAppServer(ctx context.Context, cfg AgentConfig, cwd string,
	probe func(context.Context, CommandSpec, string) error,
	start func(context.Context, string, string) error,
) (CommandSpec, error) {
	try := func(spec CommandSpec) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return probe(probeCtx, spec, cwd)
	}
	spec := cfg.AppServer
	err := try(spec)
	if err == nil || !cfg.AutoAppServer || ctx.Err() != nil {
		return spec, err
	}
	// start is idempotent: an already-running shared daemon is never restarted.
	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	startErr := start(startCtx, spec.Command, cwd)
	cancel()
	if ctx.Err() != nil {
		return spec, ctx.Err()
	}
	if startErr == nil {
		if err = try(spec); err == nil {
			return spec, nil
		}
	}
	// Older releases may support stdio but lack the managed daemon and proxy.
	spec.Args = []string{"app-server"}
	if directErr := try(spec); directErr != nil {
		return spec, fmt.Errorf("shared connection: %v; daemon start: %v; standalone: %w", err, startErr, directErr)
	}
	return spec, nil
}

func probeCodexAppServer(ctx context.Context, spec CommandSpec, cwd string) error {
	client, err := NewCodexAppServerClient(ctx, spec, cwd)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Initialize(ctx)
}

func startCodexDaemon(ctx context.Context, command, cwd string) error {
	cmd := exec.CommandContext(ctx, command, "app-server", "daemon", "start")
	cmd.Dir = cwd
	// Leave output disconnected and only cancel this launcher, never the shared
	// daemon's process group. The daemon may serve other Codex clients.
	return cmd.Run()
}

func cliTransportSupported(agent AgentName) bool {
	return agent == AgentClaude || agent == AgentCodex || agent == AgentPi
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
