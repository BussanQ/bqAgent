package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

type partialReplyClient struct {
	failingTurnClient
	seen   []map[string]any
	resume bool
}

func (c *partialReplyClient) CreateChatCompletionStream(_ context.Context, _ string, messages []map[string]any, _ []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	c.seen = messages
	if c.resume {
		return agent.AssistantMessage{Content: "继续回答海水为什么咸"}, nil
	}
	onChunk("天空呈蓝色是因为")
	onChunk("瑞利散射。")
	return agent.AssistantMessage{}, c.err
}

func TestServiceResumesInterruptedReply(t *testing.T) {
	for _, runErr := range []error{errors.New("upstream disconnected"), context.Canceled} {
		t.Run(runErr.Error(), func(t *testing.T) {
			root := t.TempDir()
			client := &partialReplyClient{failingTurnClient: failingTurnClient{runErr}}
			service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test", SystemPrompt: "test"})
			first, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "天空为什么蓝色；海水为什么咸"}, TurnOptions{Stream: true})
			if !errors.Is(err, runErr) {
				t.Fatalf("error=%v", err)
			}
			saved, err := service.store.Open(first.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := saved.LoadMessages()
			if err != nil {
				t.Fatal(err)
			}
			last := messages[len(messages)-1]
			text, _ := last["content"].(string)
			if last["role"] != "assistant" || !strings.Contains(text, "未完成的回复") || !strings.HasSuffix(text, "天空呈蓝色是因为瑞利散射。") {
				t.Fatalf("saved draft=%+v", last)
			}
			want := turn.Failed
			if errors.Is(runErr, context.Canceled) {
				want = turn.Interrupted
			}
			if saved.Meta().LastTurnOutcome.Status != want {
				t.Fatalf("outcome=%+v", saved.Meta().LastTurnOutcome)
			}
			client.resume = true
			// Recreate the service to exercise disk restoration, not in-memory history.
			service = NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test", SystemPrompt: "test"})
			second, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{SessionID: first.SessionID, Message: "继续"}, TurnOptions{Stream: true})
			if err != nil || second.Outcome.Status != turn.Completed {
				t.Fatalf("result=%+v error=%v", second, err)
			}
			found := 0
			for _, message := range client.seen {
				if message["role"] == "assistant" && message["content"] == text {
					found++
				}
			}
			if found != 1 {
				t.Fatalf("resumed draft count=%d history=%+v", found, client.seen)
			}
		})
	}
}
