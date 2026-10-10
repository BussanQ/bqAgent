package main

import (
	"context"
	"errors"
	"testing"

	"bqagent/internal/agent"
	appserver "bqagent/internal/server"
	"bqagent/internal/tools"
	apptui "bqagent/internal/tui"
)

type sessionNoticeClient struct {
	onCall func()
	fail   bool
}

func (client *sessionNoticeClient) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (agent.AssistantMessage, error) {
	client.onCall()
	if client.fail {
		return agent.AssistantMessage{}, errors.New("model unavailable")
	}
	return agent.AssistantMessage{Role: "assistant", Content: "ok"}, nil
}
func (client *sessionNoticeClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	message, err := client.CreateChatCompletion(ctx, model, messages, definitions)
	if err == nil && onChunk != nil {
		onChunk("ok")
	}
	return message, err
}

func TestTUIBackendReportsSessionBeforeModelWork(t *testing.T) {
	var notices []string
	client := &sessionNoticeClient{onCall: func() {
		if len(notices) == 0 || notices[len(notices)-1] == "" {
			t.Fatal("model started before session notice")
		}
	}}
	backend := &tuiBackend{service: appserver.NewService(appserver.ServiceOptions{
		WorkspaceRoot: t.TempDir(), AgentDir: t.TempDir(), Client: client, Model: "test-model",
	})}
	events := apptui.TurnEvents{Session: func(id string) { notices = append(notices, id) }}
	first, err := backend.RunTurn(context.Background(), "", "hello", events)
	if err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || notices[0] != first.SessionID {
		t.Fatalf("notice=%v result=%+v", notices, first)
	}
	client.fail = true
	_, err = backend.RunTurn(context.Background(), first.SessionID, "next", events)
	if err == nil || len(notices) != 2 || notices[1] != first.SessionID {
		t.Fatalf("failed turn lost canonical session: %v err=%v", notices, err)
	}
}
