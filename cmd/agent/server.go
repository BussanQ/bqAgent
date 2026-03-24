package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	appruntime "bqagent/internal/runtime"
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
	runtime := appruntime.Factory{
		Config:        appruntime.ConfigFromEnv(getenv),
		WorkspaceRoot: ws.Root,
		MemoryDir:     ws.WorkspaceMemoryDir(),
	}.Build(options.plan, true)
	service := appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot:   ws.Root,
		Client:          runtime.Client,
		Model:           runtime.Model,
		SystemPrompt:    systemPrompt,
		Planner:         runtime.Planner,
		ToolDefinitions: runtime.Catalog.Definitions(),
		Functions:       runtime.Catalog.Registry(),
	})

	botProcessor := appserver.NewBotWebhookProcessor(
		service,
		serverchanclient.NewBotClient(getenv("SERVERCHAN_BOT_TOKEN"), nil),
		serverchanclient.NewBotStateStore(ws.Root),
		getenv("SERVERCHAN_BOT_WEBHOOK_SECRET"),
	)
	channels := []appserver.Channel{
		appserver.NewServerChanChannel(service, serverchanclient.NewClient(nil), botProcessor),
	}
	if envEnabled(getenv("WEIXIN_ILINK_ENABLED")) {
		channels = append(channels, appserver.NewIlinkChannel(
			service,
			weixin.NewClientWithBaseURL(getenv("WEIXIN_ILINK_BASE_URL"), getenv("WEIXIN_ILINK_CHANNEL_VERSION"), nil),
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
			Service:  service,
			Channels: channels,
		}),
	}

	fmt.Fprintf(stdout, "server listening on %s\n", options.listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
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
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
