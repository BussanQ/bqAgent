package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/taskstate"
	"bqagent/internal/tools"
)

type taskScriptClient struct {
	responses   []agent.AssistantMessage
	messages    [][]map[string]any
	definitions [][]tools.Definition
}

func (c *taskScriptClient) CreateChatCompletion(_ context.Context, _ string, messages []map[string]any, definitions []tools.Definition) (agent.AssistantMessage, error) {
	c.messages = append(c.messages, messages)
	c.definitions = append(c.definitions, definitions)
	if len(c.responses) == 0 {
		return agent.AssistantMessage{}, fmt.Errorf("unexpected model call")
	}
	m := c.responses[0]
	c.responses = c.responses[1:]
	return m, nil
}
func (c *taskScriptClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (agent.AssistantMessage, error) {
	m, err := c.CreateChatCompletion(ctx, model, messages, definitions)
	if err == nil && onChunk != nil {
		onChunk(m.FinalContent())
	}
	return m, err
}
func serverTaskUpdate(state taskstate.State) agent.AssistantMessage {
	raw, _ := json.Marshal(state)
	args, _ := json.Marshal(map[string]any{"state": string(raw)})
	return agent.AssistantMessage{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "update", Type: "function", Function: agent.FunctionCall{Name: "task_update", Arguments: string(args)}}}}
}

func TestServiceRestoresTaskAndContinuesUntilVerified(t *testing.T) {
	root := t.TempDir()
	active := taskstate.State{Goal: "implement fast switch", Status: "active", Criteria: []taskstate.Criterion{{Description: "switch works"}}, Remaining: []string{"test switch"}}
	client := &taskScriptClient{responses: []agent.AssistantMessage{serverTaskUpdate(active), {Role: "assistant", Content: "will test"}, {Role: "assistant", Content: "will test"}, {Role: "assistant", Content: "will test"}}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test", SystemPrompt: "test"})
	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "implement fast switch"})
	if err != nil || !strings.Contains(response.Reply, "尚未确认完成") {
		t.Fatalf("first=%+v err=%v", response, err)
	}
	complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "switch test succeeded"}}, Verification: []taskstate.Verification{{Command: "test_switch", Status: "passed", Details: "ok"}}}
	client = &taskScriptClient{responses: []agent.AssistantMessage{
		{Role: "assistant", Content: "not tested yet"},
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "verify", Function: agent.FunctionCall{Name: "test_switch", Arguments: `{}`}}}},
		serverTaskUpdate(complete), {Role: "assistant", Content: "verified", Phase: "final_answer"},
	}}
	checks := 0
	service = NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test", SystemPrompt: "test", Functions: map[string]tools.Function{"test_switch": func(context.Context, map[string]any) (string, error) { checks++; return "ok", nil }}})
	result, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: response.SessionID, Message: "改完了吗"})
	if err != nil || result.Reply != "verified" || checks != 1 {
		t.Fatalf("resume=%+v checks=%d err=%v", result, checks, err)
	}
	encoded, _ := json.Marshal(client.messages[0])
	if !strings.Contains(string(encoded), active.Goal) || !strings.Contains(string(encoded), "test switch") {
		t.Fatalf("missing task snapshot: %s", encoded)
	}
	saved, err := service.store.Open(response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := saved.LoadTaskState()
	if err != nil || state.Status != "completed" || state.Goal != active.Goal || len(state.Verification) != 1 {
		t.Fatalf("task=%+v err=%v", state, err)
	}
	for _, d := range client.definitions[0] {
		if d.Function.Name == "task_update" {
			return
		}
	}
	t.Fatal("task tool missing")
}

func TestAskModeDoesNotResumeOrMutateCodingTask(t *testing.T) {
	client := &taskScriptClient{responses: []agent.AssistantMessage{{Role: "assistant", Content: "status only"}}}
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: "status", Mode: ChatModeAsk})
	if err != nil || response.Reply != "status only" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	for _, d := range client.definitions[0] {
		if d.Function.Name == "task_update" {
			t.Fatal("Ask mode exposed task mutation")
		}
	}
}
