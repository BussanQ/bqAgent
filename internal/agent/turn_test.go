package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func TestTurnOutcomeDistinguishesStops(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  turn.Status
		stage   StageConfig
		replies []AssistantMessage
		cancel  bool
	}{
		{"answer", turn.Completed, StageConfig{}, []AssistantMessage{{Content: "answer"}}, false},
		{"limit", turn.BudgetLimited, StageConfig{}, []AssistantMessage{{Content: "working", Phase: "commentary"}}, false},
		{"checkpoint", turn.BudgetLimited, StageConfig{MaxIterations: 1}, []AssistantMessage{{Content: "working", Phase: "commentary"}, {Content: "checkpoint"}}, false},
		{"failure", turn.Failed, StageConfig{}, nil, false},
		{"cancel", turn.Interrupted, StageConfig{}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			app := NewWithOptions(&stubClient{responses: tc.replies}, "test", Options{Stage: tc.stage})
			result, err := app.RunTurn(ctx, []map[string]any{{"role": "user", "content": "work"}}, 1)
			if result.Outcome.Status != tc.status {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.status == turn.BudgetLimited && (err != nil || result.Outcome.Notice == "" || result.Reply == "") {
				t.Fatalf("checkpoint lost: %+v %v", result, err)
			}
		})
	}
}

func TestResponsesEndTurnControlsContinuation(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, field := range []string{`,"end_turn":false`, `,"end_turn":true`, ``} {
			t.Run(stringBool(stream)+field, func(t *testing.T) {
				calls := 0
				client := NewClientWithAPIType("", "http://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					text, flag := "first", field
					if calls > 1 {
						text, flag = "final", `,"end_turn":true`
					}
					payload := `{"status":"completed"` + flag + `,"output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"` + text + `"}]}]}`
					if stream {
						payload = "data: {\"type\":\"response.completed\",\"response\":" + payload + "}\n\n"
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
				})})
				app := NewWithOptions(client, "test", Options{Stream: stream})
				result, err := app.RunTurn(context.Background(), nil, 3)
				wantCalls, wantReply := 1, "first"
				if strings.Contains(field, "false") {
					wantCalls, wantReply = 2, "final"
				}
				if err != nil || calls != wantCalls || result.Reply != wantReply {
					t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
				}
			})
		}
	}
}

func stringBool(b bool) string {
	if b {
		return "stream"
	}
	return "json"
}

type revisingTurnClient struct{ stubClient }

func (c *revisingTurnClient) CreateChatCompletionStreamWithOptions(ctx context.Context, model string, messages []map[string]any, defs []tools.Definition, _ ChatCompletionOptions, chunk func(string)) (AssistantMessage, error) {
	chunk("old ")
	chunk("draft")
	return c.CreateChatCompletion(ctx, model, messages, defs)
}

func TestTurnMessageSnapshotsReplaceDraftAndPreserveIdentity(t *testing.T) {
	client := &revisingTurnClient{stubClient{responses: []AssistantMessage{
		{Content: "checking", ToolCalls: []ToolCall{{ID: "read", Function: FunctionCall{Name: "read", Arguments: `{}`}}}},
		{Content: "correct answer", Phase: "final_answer"},
	}}}
	var events []turn.Event
	app := NewWithOptions(client, "test", Options{TurnID: "turn-1", Stream: true, MessageSink: func(e turn.Event) { events = append(events, e) }, Functions: map[string]tools.Function{"read": func(context.Context, map[string]any) (string, error) { return "evidence", nil }}})
	result, err := app.RunTurn(context.Background(), nil, 3)
	if err != nil || result.Reply != "correct answer" {
		t.Fatalf("%+v %v", result, err)
	}
	var projection turn.Messages
	for _, e := range events {
		projection.Apply(e)
		projection.Apply(e)
	}
	if len(projection.Items) != 2 || projection.Latest.Text != result.Reply || projection.Latest.Phase != "final_answer" {
		t.Fatalf("projection=%+v events=%+v", projection, events)
	}
	if events[0].ItemID != events[2].ItemID || events[3].ItemID == events[0].ItemID {
		t.Fatal("message identity changed between draft and snapshot")
	}
	projection.Apply(events[0])
	if projection.Latest.Text != result.Reply {
		t.Fatal("old draft replaced final")
	}
}

func TestTaskSideQuestionPreservesTaskAndResumeRestoresRelation(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(stringBool(explicit), func(t *testing.T) {
			active := activeTestTask()
			original, _ := json.Marshal(active)
			store := &memoryTaskStore{state: &active}
			client := &stubClient{}
			if !explicit {
				client.responses = append(client.responses, AssistantMessage{ToolCalls: []ToolCall{{ID: "scope", Function: FunctionCall{Name: "task_scope", Arguments: `{"relation":"side_question","reason":"用户临时询问编程语言"}`}}}})
			}
			client.responses = append(client.responses, AssistantMessage{Content: "主要用 Go"})
			options := Options{TaskStore: store}
			if explicit {
				options.TaskRelation = turn.SideQuestion
			}
			app := NewWithOptions(client, "test", options)
			result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "当前用什么语言？"}}, 4)
			actual, _ := json.Marshal(store.state)
			if err != nil || result.Reply != "主要用 Go" || result.Outcome.Status != turn.Completed || result.Outcome.TaskRelation != turn.SideQuestion || string(original) != string(actual) {
				t.Fatalf("result=%+v task=%s err=%v", result, actual, err)
			}
			client = &stubClient{responses: []AssistantMessage{{Content: "done"}}}
			app = NewWithOptions(client, "test", Options{TaskStore: store})
			resumed, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "继续"}}, 4)
			actual, _ = json.Marshal(store.state)
			if err != nil || resumed.Reply != "done" || resumed.Outcome.Status != turn.Completed || resumed.Outcome.TaskRelation != turn.ContinueTask || resumed.Outcome.TaskStatus != "active" || len(client.messages) != 1 || string(original) != string(actual) {
				t.Fatalf("resume=%+v task=%s calls=%d err=%v", resumed, actual, len(client.messages), err)
			}
		})
	}
}

func TestTaskSideQuestionCannotMutateSavedTask(t *testing.T) {
	active := activeTestTask()
	store := &memoryTaskStore{state: &active}
	client := &stubClient{responses: []AssistantMessage{taskUpdateMessage(active), {Content: "answer"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: store, TaskRelation: turn.SideQuestion})
	result, err := app.RunTurn(context.Background(), nil, 3)
	if err != nil || !reflect.DeepEqual(*store.state, active) {
		t.Fatalf("task changed: %+v %v", store.state, err)
	}
	found := false
	for _, m := range result.Messages {
		if m["role"] == "tool" && strings.Contains(contentText(m["content"]), "cannot modify") {
			found = true
		}
	}
	if !found {
		t.Fatal("task mutation was not rejected")
	}
}

func TestResponsesEmptyFinalSnapshotPreservesStreamedText(t *testing.T) {
	payload := `data: {"type":"response.output_text.delta","output_index":0,"delta":"streamed answer"}

data: {"type":"response.completed","response":{"status":"completed","output":[]}}

`
	client := NewClientWithAPIType("", "http://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})})
	message, err := client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
	if err != nil || message.FinalContent() != "streamed answer" {
		t.Fatalf("message=%+v err=%v", message, err)
	}
}

func TestTaskScopeCannotDetachAfterProgressUpdate(t *testing.T) {
	active := activeTestTask()
	store := &memoryTaskStore{state: &active}
	app := NewWithOptions(&stubClient{}, "test", Options{TaskStore: store})
	app.task = &active
	ctx := context.WithValue(context.Background(), runStateKey{}, &runState{outcome: turn.Outcome{TaskRelation: turn.ContinueTask}})
	raw, _ := json.Marshal(active)
	if _, err := app.functions["task_update"](ctx, map[string]any{"state": string(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.functions["task_scope"](ctx, map[string]any{"relation": "side_question", "reason": "avoid finishing"}); err == nil {
		t.Fatal("detached an already updated task")
	}
}
