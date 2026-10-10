package server

import (
	"bqagent/internal/agent"
	"bqagent/internal/tools"
	"context"
	"strings"
	"testing"
)

type fastRecordingClient struct {
	modelRecordingClient
	tiers []string
}

func (c *fastRecordingClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options agent.ChatCompletionOptions) (agent.AssistantMessage, error) {
	c.tiers = append(c.tiers, options.ServiceTier)
	return c.CreateChatCompletion(ctx, model, messages, definitions)
}
func TestFastCommandTogglePersistsAndReachesModel(t *testing.T) {
	client := &fastRecordingClient{}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
	id := ""
	for _, step := range []struct {
		command string
		enabled bool
	}{
		{"/fast status", false}, {"/fast", true}, {"/fast status", true}, {"/fast", false}, {"/fast on", true}, {"/fast on", true}, {"/fast off", false},
	} {
		response, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: id, Message: step.command})
		if err != nil {
			t.Fatal(err)
		}
		id = response.SessionID
		saved, err := service.store.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Meta().FastMode != step.enabled {
			t.Fatalf("%s: fast=%v", step.command, saved.Meta().FastMode)
		}
		before := len(client.tiers)
		if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: id, Message: "hello"}); err != nil {
			t.Fatal(err)
		}
		want := ""
		if step.enabled {
			want = "priority"
		}
		if len(client.tiers) != before+1 || client.tiers[before] != want {
			t.Fatalf("tiers=%v want %q", client.tiers, want)
		}
	}
	if len(client.tiers) != 7 {
		t.Fatalf("commands called model: %v", client.tiers)
	}
	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: id, Message: "/fast invalid"}); err == nil {
		t.Fatal("invalid command accepted")
	}
	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "/fast status"})
	if err != nil || !strings.Contains(response.Reply, "关闭") {
		t.Fatalf("new session: %v %v", response, err)
	}
	service.apiType = agent.APITypeAnthropic
	if _, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: id, Message: "/fast on"}); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
}
