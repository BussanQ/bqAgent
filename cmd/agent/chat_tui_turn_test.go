package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/mcp"
	appserver "bqagent/internal/server"
	"bqagent/internal/session"
	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
	apptui "bqagent/internal/tui"
	"bqagent/internal/turn"
	"bqagent/internal/workspace"
)

func TestTUIBackendRestoresPersistedTaskInHistory(t *testing.T) {
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	service := appserver.NewService(appserver.ServiceOptions{WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir()})
	store := session.NewStore(ws.Root, session.Options{AgentDir: ws.AgentDir()})
	saved, err := store.Create(session.CreateOptions{Task: "实现功能", Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	task := taskstate.State{Goal: "实现功能", Status: "active", Criteria: []taskstate.Criterion{{Description: "目标保留"}}, Remaining: []string{"验证"}}
	if err := saved.SaveTaskState(task); err != nil {
		t.Fatal(err)
	}
	backend := &tuiBackend{workspace: ws, service: service}
	history, err := backend.History(saved.ID(), apptui.ResumeHistoryBudget)
	if err != nil || history.Task == nil || history.Task.Goal != task.Goal || history.Task.Remaining[0] != "验证" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}

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

type tuiImageClient struct {
	sessionNoticeClient
	messages []map[string]any
}

func (client *tuiImageClient) CreateChatCompletionStreamWithOptions(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ agent.ChatCompletionOptions, _ func(string)) (agent.AssistantMessage, error) {
	client.messages = messages
	return agent.AssistantMessage{Role: "assistant", Content: "看到了"}, nil
}

func TestTUIBackendSendsPastedImagesAsMultimodalContent(t *testing.T) {
	client := &tuiImageClient{}
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	backend := &tuiBackend{workspace: ws, service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client, Model: "test", SystemPrompt: "test",
	})}
	var _ apptui.AttachmentBackend = backend
	result, err := backend.RunTurnWithAttachments(context.Background(), "", "描述 [图片 #1]", apptui.TurnAttachments{Images: []apptui.Image{{Label: "[图片 #1]", MIMEType: "image/png", Data: []byte("png-bytes")}}}, apptui.TurnEvents{})
	if err != nil || result.Reply != "看到了" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	for _, message := range client.messages {
		if text := fmt.Sprint(message); message["role"] == "user" && strings.Contains(text, want) && strings.Contains(text, "描述 [图片 #1]") {
			return
		}
	}
	t.Fatalf("no multimodal user message in %v", client.messages)
}

func TestTUIBackendInlinesMentionedWorkspaceFiles(t *testing.T) {
	client := &tuiImageClient{}
	ws := &workspace.Workspace{Root: t.TempDir(), GlobalAgentDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(ws.Root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "docs", "plan.md"), []byte("mention-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &tuiBackend{workspace: ws, service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client, Model: "test", SystemPrompt: "test",
	})}
	if _, err := backend.RunTurnWithAttachments(context.Background(), "", "看看 @docs/plan.md", apptui.TurnAttachments{Files: []string{"docs/plan.md"}}, apptui.TurnEvents{}); err != nil {
		t.Fatal(err)
	}
	for _, message := range client.messages {
		if text := fmt.Sprint(message); message["role"] == "user" && strings.Contains(text, `path="docs/plan.md"`) && strings.Contains(text, "mention-body") {
			return
		}
	}
	t.Fatalf("mentioned file was not inlined: %v", client.messages)
}
