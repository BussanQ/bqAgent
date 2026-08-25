package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bqagent/internal/agent"
	"bqagent/internal/qq"
	appserver "bqagent/internal/server"
	serverchanclient "bqagent/internal/serverchan"
	"bqagent/internal/weixin"
	"bqagent/internal/workspace"
)

func runServerInBackground(stdout, stderr io.Writer, deps runDeps, ws *workspace.Workspace, options cliOptions) int {
	outputPath, err := serverOutputPath(ws)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	executable, err := deps.executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	childArgs := []string{"--server-run", "--listen", options.listen}
	if options.plan {
		childArgs = append(childArgs, "--plan")
	}
	if err := deps.startBackground(executable, childArgs, ws.Root, outputPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintf(stdout, "listen: %s\noutput_log: %s\n", options.listen, outputPath)
	return 0
}

func runServer(ctx context.Context, stdout, stderr io.Writer, getenv func(string) string, ws *workspace.Workspace, systemPrompt string, options cliOptions) int {
	configureServerChannelLimits(stderr, getenv)

	service, externalBroker := newConversationService(ctx, getenv, ws, systemPrompt, options.plan, stdout)
	workspaceRegistry, err := appserver.NewWorkspaceRegistry(appserver.WorkspaceRegistryOptions{
		DefaultRoot:    ws.Root,
		DefaultService: service,
		DefaultCloser:  externalBroker,
		AllowedRoots: appserver.DefaultWorkspaceAllowedRoots(
			ws.Root,
			appserver.SplitWorkspaceRoots(getenv("WEBUI_WORKSPACE_ROOTS")),
		),
		Factory: func(factoryContext context.Context, root string) (*appserver.Service, io.Closer, error) {
			selected := &workspace.Workspace{Root: root, GlobalAgentDir: ws.AgentDir()}
			prompt, promptErr := selected.BuildSystemPrompt("")
			if promptErr != nil {
				return nil, nil, promptErr
			}
			selectedService, broker := newConversationService(factoryContext, getenv, selected, prompt, options.plan, stdout)
			return selectedService, broker, nil
		},
	})
	if err != nil {
		_ = externalBroker.Close()
		fmt.Fprintln(stderr, err)
		return 1
	}

	botProcessor := appserver.NewBotWebhookProcessor(
		service,
		serverchanclient.NewBotClient(getenv("SERVERCHAN_BOT_TOKEN"), nil),
		serverchanclient.NewBotStateStore(ws.Root),
		getenv("SERVERCHAN_BOT_WEBHOOK_SECRET"),
	)
	channels := []appserver.Channel{
		appserver.NewServerChanChannel(service, serverchanclient.NewClient(nil), botProcessor),
		appserver.NewWebUIChannelWithWorkspaces(service, workspaceRegistry, envEnabled(getenv("WEBUI_ENABLED"))),
	}
	if qqBotEnabled(getenv) {
		tokenClient := qq.NewTokenClient(getenv("QQ_BOT_APP_ID"), getenv("QQ_BOT_CLIENT_SECRET"), getenv("QQ_BOT_TOKEN_BASE_URL"), nil)
		tokenSource := qq.NewCachedTokenSource(tokenClient)
		apiBaseURL := getenv("QQ_BOT_API_BASE_URL")
		channels = append(channels, appserver.NewQQChannel(
			service,
			qq.NewClient(tokenSource, apiBaseURL, nil),
			qq.NewGatewayClient(tokenSource, apiBaseURL, nil),
			qq.NewStateStore(ws.Root),
			qq.NewGatewayStateStore(ws.Root),
		))
	}
	if envEnabled(getenv("WEIXIN_ILINK_ENABLED")) {
		ilinkClient := weixin.NewClientWithBaseURL(getenv("WEIXIN_ILINK_BASE_URL"), getenv("WEIXIN_ILINK_CHANNEL_VERSION"), nil)
		ilinkClient.SetCDNBaseURL(getenv("WEIXIN_ILINK_CDN_BASE_URL"))
		channels = append(channels, appserver.NewIlinkChannel(
			service,
			ilinkClient,
			weixin.NewTokenStore(ws.Root),
			weixin.NewPollerStateStore(ws.Root),
			weixin.NewChatStateStore(ws.Root),
		))
	}
	for _, channel := range channels {
		if channel == nil || !channel.Enabled() {
			continue
		}
		channel.Start(ctx)
	}

	server := &http.Server{
		Addr: options.listen,
		Handler: appserver.NewHandler(appserver.HandlerOptions{
			Service:    service,
			Workspaces: workspaceRegistry,
			Channels:   channels,
		}),
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	fmt.Fprintf(stdout, "server listening on %s\n", options.listen)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()

	select {
	case err := <-serveResult:
		_ = workspaceRegistry.Close()
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case <-ctx.Done():
		// Close channel admission before waiting for channel goroutines. This
		// establishes the required happens-before edge for WaitGroup.Add/Wait.
		stopChannelTurns(channels)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout(getenv))
		shutdownErr := server.Shutdown(shutdownCtx)
		waitForChannelTurns(shutdownCtx, channels)
		cancel()
		// Channel turn runners own their keyed locks; closing the broker only after
		// they have drained avoids racing a live ACP request or resurrecting its
		// client during shutdown.
		_ = workspaceRegistry.Close()
		if shutdownErr != nil && shutdownErr != context.DeadlineExceeded {
			fmt.Fprintf(stderr, "server shutdown: %v\n", shutdownErr)
		}
		return 0
	}
}

const defaultServerShutdownTimeout = 15 * time.Second

func serverShutdownTimeout(getenv func(string) string) time.Duration {
	if raw := strings.TrimSpace(getenv("SERVER_SHUTDOWN_TIMEOUT")); raw != "" {
		if timeout, err := time.ParseDuration(raw); err == nil && timeout > 0 {
			return timeout
		}
	}
	return defaultServerShutdownTimeout
}

// stopChannelTurns closes dispatch admission before graceful shutdown waits
// for each channel's in-flight goroutines.
func stopChannelTurns(channels []appserver.Channel) {
	for _, channel := range channels {
		if stopper, ok := channel.(interface{ StopAcceptingTurns() }); ok {
			stopper.StopAcceptingTurns()
		}
	}
}

// waitForChannelTurns waits only up to ctx's deadline. A broken external
// channel must not keep process shutdown blocked forever.
func waitForChannelTurns(ctx context.Context, channels []appserver.Channel) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, channel := range channels {
			if waiter, ok := channel.(interface{ WaitTurns() }); ok {
				waiter.WaitTurns()
			}
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func configureServerChannelLimits(stderr io.Writer, getenv func(string) string) {
	if raw := strings.TrimSpace(getenv("CHANNEL_TURN_TIMEOUT")); raw != "" {
		if timeout, err := time.ParseDuration(raw); err == nil && timeout > 0 {
			appserver.SetChannelTurnTimeout(timeout)
		} else {
			fmt.Fprintf(stderr, "invalid CHANNEL_TURN_TIMEOUT %q, using default\n", raw)
		}
	}
	if raw := strings.TrimSpace(getenv("CHANNEL_AGENT_MAX_ITERATIONS")); raw != "" {
		if maxIterations, err := strconv.Atoi(raw); err == nil && maxIterations > 0 {
			appserver.SetChannelMaxIterations(maxIterations)
		} else {
			fmt.Fprintf(stderr, "invalid CHANNEL_AGENT_MAX_ITERATIONS %q, using default\n", raw)
		}
	}
	if raw := strings.TrimSpace(getenv("CHANNEL_STAGE_TIMEOUT")); raw != "" {
		if timeout, err := time.ParseDuration(raw); err == nil && timeout > 0 {
			appserver.SetChannelStageTimeout(timeout)
		} else {
			fmt.Fprintf(stderr, "invalid CHANNEL_STAGE_TIMEOUT %q, using default\n", raw)
		}
	}
	if raw := strings.TrimSpace(getenv("CHANNEL_STAGE_MAX_ITERATIONS")); raw != "" {
		if maxIterations, err := strconv.Atoi(raw); err == nil && maxIterations > 0 {
			appserver.SetChannelStageMaxIterations(maxIterations)
		} else {
			fmt.Fprintf(stderr, "invalid CHANNEL_STAGE_MAX_ITERATIONS %q, using default\n", raw)
		}
	}
	if raw := strings.TrimSpace(getenv("WEBUI_STAGE_TIMEOUT")); raw != "" {
		if timeout, err := time.ParseDuration(raw); err == nil && timeout > 0 {
			appserver.SetWebUIStageTimeout(timeout)
		} else {
			fmt.Fprintf(stderr, "invalid WEBUI_STAGE_TIMEOUT %q, using default\n", raw)
		}
	}
	if raw := strings.TrimSpace(getenv("WEBUI_STAGE_MAX_ITERATIONS")); raw != "" {
		if maxIterations, err := strconv.Atoi(raw); err == nil && maxIterations > 0 {
			appserver.SetWebUIStageMaxIterations(maxIterations)
		} else {
			fmt.Fprintf(stderr, "invalid WEBUI_STAGE_MAX_ITERATIONS %q, using default\n", raw)
		}
	}
}

func serverOutputPath(ws *workspace.Workspace) (string, error) {
	dir := filepath.Join(ws.AgentDir(), "server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "server.log"), nil
}

func envEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return true
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envEnabledStrict(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func qqBotEnabled(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("QQ_BOT_ENABLED"))) {
	case "0", "false", "no", "off":
		return false
	}
	return strings.TrimSpace(getenv("QQ_BOT_APP_ID")) != "" && strings.TrimSpace(getenv("QQ_BOT_CLIENT_SECRET")) != ""
}
