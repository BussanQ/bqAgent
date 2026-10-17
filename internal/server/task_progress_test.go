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
	client := &taskScriptClient{responses: []agent.AssistantMessage{serverTaskUpdate(active), {Role: "assistant", Content: "will test", Phase: "commentary"}, {Role: "assistant", Content: "will test", Phase: "commentary"}, {Role: "assistant", Content: "will test", Phase: "commentary"}}}
	service := NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test", SystemPrompt: "test"})
	response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "implement fast switch"}, TurnOptions{MaxIterations: 4})
	if err != nil || !strings.Contains(response.Reply, "maximum of 4 iterations") {
		t.Fatalf("first=%+v err=%v", response, err)
	}
	complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "switch test succeeded"}}, Verification: []taskstate.Verification{{Command: "go test ./switch", Status: "passed", Details: "ok"}}}
	client = &taskScriptClient{responses: []agent.AssistantMessage{
		{Role: "assistant", Content: "not tested yet", Phase: "commentary"},
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "verify", Function: agent.FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test ./switch"}`}}}},
		serverTaskUpdate(complete), {Role: "assistant", Content: "verified", Phase: "final_answer"},
	}}
	checks := 0
	service = NewService(ServiceOptions{WorkspaceRoot: root, Client: client, Model: "test", SystemPrompt: "test", Functions: map[string]tools.Function{"execute_bash": func(context.Context, map[string]any) (string, error) { checks++; return "ok", nil }}})
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

func TestServiceCompletesLongTaskWithoutFollowupTurn(t *testing.T) {
	active := taskstate.State{Goal: "implement fast switch", Status: "active", Criteria: []taskstate.Criterion{{Description: "switch works"}}, Remaining: []string{"implement", "verify"}}
	responses := []agent.AssistantMessage{serverTaskUpdate(active)}
	for i := 0; i < 6; i++ {
		responses = append(responses,
			agent.AssistantMessage{Content: "still working", Phase: "commentary"},
			agent.AssistantMessage{Content: ""},
			agent.AssistantMessage{Content: ""},
			agent.AssistantMessage{Content: "still working", Phase: "commentary"},
			agent.AssistantMessage{ToolCalls: []agent.ToolCall{{ID: fmt.Sprintf("edit-%d", i), Function: agent.FunctionCall{Name: "edit_switch", Arguments: fmt.Sprintf(`{"step":%d}`, i)}}}})
	}
	complete := taskstate.State{Goal: active.Goal, Status: "completed", Criteria: []taskstate.Criterion{{Description: "switch works", Done: true, Evidence: "switch verification passed"}}, Verification: []taskstate.Verification{{Command: "go test ./switch", Status: "passed", Details: "ok"}}}
	responses = append(responses,
		agent.AssistantMessage{ToolCalls: []agent.ToolCall{{ID: "verify", Function: agent.FunctionCall{Name: "execute_bash", Arguments: `{"command":"go test ./switch"}`}}}},
		serverTaskUpdate(complete), agent.AssistantMessage{Content: "verified", Phase: "final_answer"})
	client := &taskScriptClient{responses: responses}
	edits, checks := 0, 0
	service := NewService(ServiceOptions{WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test", Functions: map[string]tools.Function{
		"edit_switch":  func(context.Context, map[string]any) (string, error) { edits++; return "", nil },
		"execute_bash": func(context.Context, map[string]any) (string, error) { checks++; return "ok", nil },
	}})
	response, err := service.HandleTurn(context.Background(), TurnRequest{Message: active.Goal})
	if err != nil || response.Reply != "verified" || edits != 6 || checks != 1 || len(client.responses) != 0 {
		t.Fatalf("response=%+v edits=%d checks=%d remaining=%d err=%v", response, edits, checks, len(client.responses), err)
	}
	saved, err := service.store.Open(response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := saved.LoadTaskState()
	if err != nil || state == nil || state.Status != "completed" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
