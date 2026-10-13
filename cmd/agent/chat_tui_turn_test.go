package main

import (
	"context"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/mcp"
	appserver "bqagent/internal/server"
	"bqagent/internal/tools"
	apptui "bqagent/internal/tui"
	"bqagent/internal/turn"
	"bqagent/internal/workspace"
)

func TestTUIBackendForwardsSharedTurnContract(t *testing.T) {
	client := &tuiEffortClient{}
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	backend := &tuiBackend{workspace: ws, service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client, Model: "test", SystemPrompt: "test",
		Functions: map[string]tools.Function{"lookup": func(context.Context, map[string]any) (string, error) { return "found", nil }},
	})}
	var messages turn.Messages
	legacyTokens := 0
	result, err := backend.RunTurn(context.Background(), "", "what language", apptui.TurnEvents{
		Message: func(e turn.Event) { messages.Apply(e) }, Token: func(string) { legacyTokens++ },
	})
	if err != nil || result.Outcome.Status != turn.Completed || result.TurnID == "" || result.TurnID != messages.TurnID || messages.Latest.Text != result.Reply || legacyTokens != 0 {
		t.Fatalf("result=%+v messages=%+v legacy=%d err=%v", result, messages, legacyTokens, err)
	}
}

func TestTUIBackendForwardsContextUsage(t *testing.T) {
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	backend := &tuiBackend{workspace: ws, service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: &tuiEffortClient{}, Model: "test", SystemPrompt: "test",
		Context:   agent.DefaultContextConfig(),
		Functions: map[string]tools.Function{"lookup": func(context.Context, map[string]any) (string, error) { return "found", nil }},
	})}
	var snapshots []turn.ContextUsage
	_, err := backend.RunTurn(context.Background(), "", "question", apptui.TurnEvents{
		Context: func(usage turn.ContextUsage) { snapshots = append(snapshots, usage) },
	})
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	for _, usage := range snapshots {
		if usage.Tokens <= 0 || usage.Limit != 200000 || !usage.Estimated {
			t.Fatalf("invalid context usage: %+v", usage)
		}
	}
}

func TestTUIBackendForwardsMCPStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	discovery := mcp.StartDiscovery(ctx, mcp.Config{Servers: map[string]mcp.ServerConfig{"disabled": {Disabled: true}}}, nil, nil, nil, nil, 0)
	<-discovery.Done()
	backend := &tuiBackend{service: appserver.NewService(appserver.ServiceOptions{WorkspaceRoot: t.TempDir(), MCP: discovery})}
	if statuses := backend.MCPStatus(); len(statuses) != 1 || statuses[0].State != "disabled" {
		t.Fatalf("statuses = %#v", statuses)
	}
}
