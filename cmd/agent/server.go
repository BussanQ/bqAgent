package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	serverchanclient "bqagent/internal/serverchan"
	"bqagent/internal/tools"
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
	chatClient := agent.NewClient(getenv("OPENAI_API_KEY"), getenv("OPENAI_BASE_URL"), nil)
	var planner *agent.Planner
	if options.plan {
		planner = agent.NewPlanner(chatClient, getenv("OPENAI_MODEL"))
	}
	catalog := tools.NewCatalog(tools.Options{
		WorkspaceRoot: ws.Root,
		IncludePlan:   options.plan,
		SearchAPIKey:  getenv("SEARCH_API_KEY"),
		SearchBaseURL: getenv("SEARCH_BASE_URL"),
		MemoryDir:     ws.WorkspaceMemoryDir(),
		ServerMode:    true,
	})
	service := appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot:   ws.Root,
		Client:          chatClient,
		Model:           getenv("OPENAI_MODEL"),
		SystemPrompt:    systemPrompt,
		Planner:         planner,
		ToolDefinitions: catalog.Definitions(),
		Functions:       catalog.Registry(),
	})
	botProcessor := appserver.NewBotWebhookProcessor(
		service,
		serverchanclient.NewBotClient(getenv("SERVERCHAN_BOT_TOKEN"), nil),
		serverchanclient.NewBotStateStore(ws.Root),
		getenv("SERVERCHAN_BOT_WEBHOOK_SECRET"),
	)
	server := &http.Server{
		Addr: options.listen,
		Handler: appserver.NewHandler(appserver.HandlerOptions{
			Service:             service,
			ServerChanClient:    serverchanclient.NewClient(nil),
			BotWebhookProcessor: botProcessor,
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
