package server

import (
	"bqagent/internal/tools"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/agent"
	"bqagent/internal/taskstate"
	"bqagent/internal/turn"
)

func TestServicePersistsTurnOutcomeIndependentOfTaskProgress(t *testing.T) {
	for _, status := range []turn.Status{turn.Completed, turn.BudgetLimited, turn.Blocked, turn.AwaitingInput} {
		t.Run(string(status), func(t *testing.T) {
			client := &taskScriptClient{}
			if status == turn.Blocked || status == turn.AwaitingInput {
				state := taskstate.State{Goal: "implement", Status: string(status), Criteria: []taskstate.Criterion{{Description: "works"}}, Remaining: []string{"implement"}, Reason: "missing user input"}
				client.responses = append(client.responses, serverTaskUpdate(state))
			}
			phase := "final_answer"
			if status == turn.BudgetLimited {
				phase = "commentary"
			}
			client.responses = append(client.responses, agent.AssistantMessage{Content: "answer", Phase: phase})
			service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
			result, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "work", TurnID: "test-turn"}, TurnOptions{MaxIterations: len(client.responses)})
			want := status
			if status == turn.Blocked || status == turn.AwaitingInput {
				if result.Outcome.TaskStatus != string(status) {
					t.Fatalf("task progress status lost: %+v", result.Outcome)
				}
			}
			if err != nil || result.Outcome.Status != want || result.TurnID != "test-turn" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			saved, err := service.store.Open(result.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if string(saved.Meta().Status) != string(want) || saved.Meta().LastTurnOutcome == nil || saved.Meta().LastTurnOutcome.Status != want {
				t.Fatalf("meta=%+v", saved.Meta())
			}
		})
	}
}

func TestWebUIUsesSharedMessageSnapshotsAndOutcome(t *testing.T) {
	client := &taskScriptClient{responses: []agent.AssistantMessage{{Content: "checking", Phase: "commentary"}, {Content: "主要用 Go", Phase: "final_answer"}}}
	service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
	channel := NewWebUIChannel(service, true)
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/webui/chat", strings.NewReader(`{"message":"用什么语言","turn_id":"web-turn"}`))
	request.Header.Set("Content-Type", "application/json")
	channel.handleStreamChat(response, request)
	var projection turn.Messages
	var done webUIDoneEvent
	for _, raw := range strings.Split(response.Body.String(), "\n\n") {
		lines := strings.Split(raw, "\n")
		if len(lines) < 2 {
			continue
		}
		data := strings.TrimPrefix(lines[1], "data: ")
		switch lines[0] {
		case "event: message_snapshot":
			var e turn.Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatal(err)
			}
			projection.Apply(e)
			projection.Apply(e)
		case "event: done":
			if err := json.Unmarshal([]byte(data), &done); err != nil {
				t.Fatal(err)
			}
		}
	}
	if done.Outcome.Status != turn.Completed || done.TurnID != "web-turn" || done.Reply != "主要用 Go" || projection.Latest.Text != done.Reply || len(projection.Items) != 2 {
		t.Fatalf("done=%+v projection=%+v wire=%s", done, projection, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "event: message\n") {
		t.Fatal("both legacy deltas and snapshots emitted")
	}
}

func TestServiceSideQuestionDoesNotPauseSavedTask(t *testing.T) {
	client := &taskScriptClient{responses: []agent.AssistantMessage{{Content: "working", Phase: "commentary"}}}
	service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
	first, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "implement"}, TurnOptions{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := service.store.Open(first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	active := taskstate.State{Goal: "implement", Status: "active", Criteria: []taskstate.Criterion{{Description: "works"}}, Remaining: []string{"verify"}}
	if err = saved.SaveTaskState(active); err != nil {
		t.Fatal(err)
	}
	client.responses = []agent.AssistantMessage{{Content: "主要用 Go"}}
	result, err := service.HandleTurn(context.Background(), TurnRequest{SessionID: first.SessionID, Message: "什么语言", TaskRelation: turn.SideQuestion})
	if err != nil || result.Outcome.Status != turn.Completed || result.Outcome.TaskStatus != "active" {
		t.Fatalf("result=%+v %v", result, err)
	}
	saved, err = service.store.Open(first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := saved.LoadTaskState()
	if err != nil || state.Status != "active" || state.Goal != active.Goal || len(state.Remaining) != 1 {
		t.Fatalf("state=%+v %v", state, err)
	}
}

type failingTurnClient struct{ err error }

func (c failingTurnClient) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (agent.AssistantMessage, error) {
	return agent.AssistantMessage{}, c.err
}
func (c failingTurnClient) CreateChatCompletionStream(context.Context, string, []map[string]any, []tools.Definition, func(string)) (agent.AssistantMessage, error) {
	return agent.AssistantMessage{}, c.err
}

func TestServiceFailureAndCancellationAreNotCompleted(t *testing.T) {
	for _, runErr := range []error{errors.New("provider failed"), context.Canceled} {
		t.Run(runErr.Error(), func(t *testing.T) {
			service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), Client: failingTurnClient{runErr}, Model: "test", SystemPrompt: "test"})
			var sessionID string
			response, err := service.HandleTurnWithOptions(context.Background(), TurnRequest{Message: "work"}, TurnOptions{SessionSink: func(id string) { sessionID = id }})
			want := turn.Failed
			if errors.Is(runErr, context.Canceled) {
				want = turn.Interrupted
			}
			if !errors.Is(err, runErr) || response.Outcome.Status != want {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			saved, err := service.store.Open(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if string(saved.Meta().Status) != string(want) || saved.Meta().LastTurnOutcome == nil || saved.Meta().LastTurnOutcome.Status != want {
				t.Fatalf("meta=%+v", saved.Meta())
			}
		})
	}
}

func TestWebUIBudgetStopUsesBackendOutcome(t *testing.T) {
	old := WebUIStageMaxIterations()
	SetWebUIStageMaxIterations(1)
	t.Cleanup(func() { SetWebUIStageMaxIterations(old) })
	client := &taskScriptClient{responses: []agent.AssistantMessage{{Content: "working", Phase: "commentary"}, {Content: "还需分析"}}}
	service := NewService(ServiceOptions{TaskToolEnabled: true, WorkspaceRoot: t.TempDir(), Client: client, Model: "test", SystemPrompt: "test"})
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/webui/chat", strings.NewReader(`{"message":"analyze"}`))
	request.Header.Set("Content-Type", "application/json")
	NewWebUIChannel(service, true).handleStreamChat(response, request)
	if !strings.Contains(response.Body.String(), `"status":"budget_limited"`) || !strings.Contains(response.Body.String(), `"reply":"还需分析"`) || !strings.Contains(response.Body.String(), `"notice":"本轮已达到执行限制，任务尚未完成。"`) {
		t.Fatalf("wire=%s", response.Body.String())
	}
}
