package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	if strings.TrimSpace(getenv("OPENAI_API_KEY")) == "" {
		fmt.Fprintln(stderr, "OPENAI_API_KEY is required for server mode")
		return 1
	}

	if raw := strings.TrimSpace(getenv("CHANNEL_TURN_TIMEOUT")); raw != "" {
		if timeout, err := time.ParseDuration(raw); err == nil && timeout > 0 {
			appserver.SetChannelTurnTimeout(timeout)
		} else {
			fmt.Fprintf(stderr, "invalid CHANNEL_TURN_TIMEOUT %q, using default\n", raw)
		}
	}

	service, externalBroker := newConversationService(ctx, getenv, ws, systemPrompt, options.plan, stdout)
	defer externalBroker.Close()

	botProcessor := appserver.NewBotWebhookProcessor(
		service,
		serverchanclient.NewBotClient(getenv("SERVERCHAN_BOT_TOKEN"), nil),
		serverchanclient.NewBotStateStore(ws.Root),
		getenv("SERVERCHAN_BOT_WEBHOOK_SECRET"),
	)
	channels := []appserver.Channel{
		appserver.NewServerChanChannel(service, serverchanclient.NewClient(nil), botProcessor),
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
