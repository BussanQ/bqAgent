package server

import (
	"context"
	"strings"
	"sync"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
)

type modelRecordingClient struct {
	mu       sync.Mutex
	models   []string
	messages [][]map[string]any
}

func (client *modelRecordingClient) CreateChatCompletion(_ context.Context, model string, messages []map[string]any, _ []tools.Definition) (agent.AssistantMessage, error) {
	client.mu.Lock()
	client.models = append(client.models, model)
	client.messages = append(client.messages, append([]map[string]any(nil), messages...))
	client.mu.Unlock()
	return agent.AssistantMessage{Role: "assistant", Content: "ok"}, nil
}

func (client *modelRecordingClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	if onChunk != nil {
		onChunk("ok")
	}
	return client.CreateChatCompletion(ctx, model, messages, definitions)
}

func TestModelCommandPersistsSelectionAndUsesIt(t *testing.T) {
	root := t.TempDir()
	client := &modelRecordingClient{}
	service := NewService(ServiceOptions{
		WorkspaceRoot: root,
		Client:        client,
		Model:         "default-model",
		Models:        []string{"fast=selected-model", "other-model"},
		SystemPrompt:  "base prompt",
	})

	switchResponse, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/model fast"})
	if err != nil {
		t.Fatalf("switch model returned error: %v", err)
	}
	if !strings.Contains(switchResponse.Reply, "selected-model") {
		t.Fatalf("switch reply = %q, want selected model", switchResponse.Reply)
	}
	if switchResponse.Model != "selected-model" {
		t.Fatalf("switch response model = %q, want selected-model", switchResponse.Model)
	}
	if info := service.RuntimeLLMInfoForSession(switchResponse.SessionID); info.Model != "selected-model" {
		t.Fatalf("session runtime model = %q, want selected-model", info.Model)
	}
	if len(client.models) != 0 {
		t.Fatalf("model command called LLM %d times, want 0", len(client.models))
	}

	savedSession, err := service.store.Open(switchResponse.SessionID)
	if err != nil {
		t.Fatalf("open session returned error: %v", err)
	}
	if savedSession.Meta().CurrentModel != "selected-model" {
		t.Fatalf("CurrentModel = %q, want selected-model", savedSession.Meta().CurrentModel)
	}
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatalf("load messages returned error: %v", err)
	}
	if len(messages) != 3 || messages[1]["content"] != "/model fast" {
		t.Fatalf("command transcript = %#v, want system, user, and assistant messages", messages)
	}

	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: switchResponse.SessionID, Message: "hello"}); err != nil {
		t.Fatalf("follow-up returned error: %v", err)
	}
	if len(client.models) != 1 || client.models[0] != "selected-model" {
		t.Fatalf("models = %#v, want selected-model", client.models)
	}
	if len(client.messages[0]) == 0 || !strings.Contains(messageText(client.messages[0][0]), "selected-model") {
		t.Fatalf("system prompt = %#v, want selected model identity", client.messages[0])
	}
}

func TestModelCommandListsAndRestoresDefault(t *testing.T) {
	service := NewService(ServiceOptions{
		WorkspaceRoot: t.TempDir(),
		Model:         "default-model",
		Models:        []string{"fast=selected-model", "other-model"},
		SystemPrompt:  "base prompt",
	})

	listResponse, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/model"})
	if err != nil {
		t.Fatalf("list models returned error: %v", err)
	}
	if !strings.Contains(listResponse.Reply, "fast = selected-model") || !strings.Contains(listResponse.Reply, "当前模型：default-model") {
		t.Fatalf("list reply = %q", listResponse.Reply)
	}

	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: listResponse.SessionID, Message: "/model fast"}); err != nil {
		t.Fatalf("switch model returned error: %v", err)
	}
	resetResponse, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: listResponse.SessionID, Message: "/model default"})
	if err != nil {
		t.Fatalf("reset model returned error: %v", err)
	}
	if !strings.Contains(resetResponse.Reply, "default-model") {
		t.Fatalf("reset reply = %q", resetResponse.Reply)
	}
	if resetResponse.Model != "default-model" {
		t.Fatalf("reset response model = %q, want default-model", resetResponse.Model)
	}
	savedSession, err := service.store.Open(listResponse.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if savedSession.Meta().CurrentModel != "" {
		t.Fatalf("CurrentModel = %q, want empty", savedSession.Meta().CurrentModel)
	}
}

func messageText(message map[string]any) string {
	content, _ := message["content"].(string)
	return content
}
