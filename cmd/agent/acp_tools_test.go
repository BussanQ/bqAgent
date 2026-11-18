package main

import (
	"context"
	"io"
	"strings"
	"testing"

	"bqagent/internal/acp"
	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	"bqagent/internal/tools"
	"bqagent/internal/workspace"
)

type acpToolClient struct {
	sessionNoticeClient
	calls          int
	toolResultSeen bool
}

func (client *acpToolClient) CreateChatCompletionStreamWithOptions(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, _ agent.ChatCompletionOptions, onChunk func(string)) (agent.AssistantMessage, error) {
	client.calls++
	if client.calls == 1 {
		return agent.AssistantMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{
			ID: "read-one", Type: "function", Function: agent.FunctionCall{Name: "read_file", Arguments: `{"path":"example.txt"}`},
		}}}, nil
	}
	client.toolResultSeen = strings.Contains(stringifyMessages(messages), "file contents")
	onChunk("read complete")
	return agent.AssistantMessage{Role: "assistant", Content: "read complete"}, nil
}

func TestACPBackendExecutesToolsWithoutApproval(t *testing.T) {
	client := &acpToolClient{}
	backend, root := newACPTestBackend(t, client)
	called := false
	backend.newService = func(_ context.Context, _ func(string) string, ws *workspace.Workspace, _ string, _ io.Writer) (*appserver.Service, func()) {
		return appserver.NewService(appserver.ServiceOptions{
			WorkspaceRoot: ws.Root, AgentDir: ws.AgentDir(), Client: client,
			Model: "model-a", SystemPrompt: "test",
			ToolDefinitions: []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "read_file", Parameters: tools.JSONSchema{Type: "object"}}}},
			Functions: map[string]tools.Function{"read_file": func(_ context.Context, args map[string]any) (string, error) {
				called = args["path"] == "example.txt"
				return "file contents", nil
			}},
		}), nil
	}
	conversation, err := backend.Open(context.Background(), root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	var updates []acp.Update
	stop, err := conversation.Prompt(context.Background(), []acp.ContentBlock{{Type: "text", Text: "read example.txt"}}, func(update acp.Update) error {
		updates = append(updates, update)
		return nil
	})
	if err != nil || stop != "end_turn" || !called || !client.toolResultSeen {
		t.Fatalf("stop=%q err=%v called=%v resultSeen=%v", stop, err, called, client.toolResultSeen)
	}
	var started, completed bool
	for _, update := range updates {
		switch update["sessionUpdate"] {
		case "tool_call":
			started = update["toolCallId"] == "read-one" && update["status"] == "in_progress"
		case "tool_call_update":
			completed = update["toolCallId"] == "read-one" && update["status"] == "completed"
		case "agent_message_chunk":
		default:
			t.Fatalf("unexpected interaction: %+v", update)
		}
	}
	if !started || !completed {
		t.Fatalf("missing tool lifecycle: %+v", updates)
	}
	updates = nil
	if err := conversation.Replay(context.Background(), func(update acp.Update) error { updates = append(updates, update); return nil }); err != nil {
		t.Fatal(err)
	}
	var restored bool
	for _, update := range updates {
		if update["sessionUpdate"] == "tool_call" && update["title"] == "read_file" && update["status"] == "completed" {
			restored = true
		}
	}
	if !restored {
		t.Fatalf("tool missing from history: %+v", updates)
	}
}
