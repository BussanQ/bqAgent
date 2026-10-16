package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func TestTurnSteeringAppliesAfterToolsBeforeFollowUp(t *testing.T) {
	q := &turn.InputQueue{}
	_ = q.Add(turn.Input{ID: "later", Kind: turn.FollowUp, Text: "summarize afterwards"})
	client := &stubClient{responses: []AssistantMessage{
		{ToolCalls: []ToolCall{{ID: "read", Function: FunctionCall{Name: "read_file", Arguments: `{}`}}}},
		{Content: "corrected result"},
		{Content: "summary"},
	}}
	var events []turn.Event
	app := NewWithOptions(client, "test", Options{MessageSink: func(e turn.Event) { events = append(events, e) }, Functions: map[string]tools.Function{
		"read_file": func(context.Context, map[string]any) (string, error) {
			if err := q.Add(turn.Input{ID: "correction", Kind: turn.Steering, Text: "only analyze"}); err != nil {
				t.Error(err)
			}
			return "source", nil
		},
	}})
	result, err := app.RunTurn(turn.WithInputQueue(context.Background(), q), []map[string]any{{"role": "user", "content": "original task"}}, 5)
	if err != nil || result.Reply != "summary" || len(client.messages) != 3 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, len(client.messages))
	}
	for i, request := range client.messages {
		var text strings.Builder
		for _, message := range request {
			text.WriteString(contentText(message["content"]))
		}
		if strings.Contains(text.String(), "only analyze") != (i >= 1) || strings.Contains(text.String(), "summarize afterwards") != (i >= 2) {
			t.Fatalf("request %d input order wrong: %s", i, text.String())
		}
	}
	var applied []string
	for _, event := range events {
		if event.Kind == "input.applied" {
			applied = append(applied, event.ItemID)
		}
	}
	if len(applied) != 2 || applied[0] != "correction" || applied[1] != "later" {
		t.Fatalf("applied=%v", applied)
	}
	if !errors.Is(q.Add(turn.Input{Kind: turn.Steering, Text: "too late"}), turn.ErrInputClosed) {
		t.Fatal("finished turn still accepts input")
	}
}

type inputDuringResponseClient struct {
	stubClient
	queue *turn.InputQueue
}

func (c *inputDuringResponseClient) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, defs []tools.Definition) (AssistantMessage, error) {
	if len(c.messages) == 0 {
		if err := c.queue.Add(turn.Input{ID: "late", Kind: turn.Steering, Text: "revise before finishing"}); err != nil {
			return AssistantMessage{}, err
		}
	}
	return c.stubClient.CreateChatCompletion(ctx, model, messages, defs)
}

func (c *inputDuringResponseClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, defs []tools.Definition, _ ChatCompletionOptions) (AssistantMessage, error) {
	return c.CreateChatCompletion(ctx, model, messages, defs)
}

func TestTurnAcceptsSteeringDuringFinalResponse(t *testing.T) {
	q := &turn.InputQueue{}
	client := &inputDuringResponseClient{queue: q, stubClient: stubClient{responses: []AssistantMessage{{Content: "old answer"}, {Content: "revised answer"}}}}
	app := NewWithOptions(client, "test", Options{})
	result, err := app.RunTurn(turn.WithInputQueue(context.Background(), q), []map[string]any{{"role": "user", "content": "original"}}, 4)
	if err != nil || result.Reply != "revised answer" || len(client.messages) != 2 {
		t.Fatalf("result=%+v requests=%d err=%v", result, len(client.messages), err)
	}
	found := false
	for _, message := range result.Messages {
		if message["role"] == "user" && contentText(message["content"]) == "revise before finishing" {
			found = true
		}
	}
	if !found {
		t.Fatal("accepted correction missing from persisted history")
	}
}

func TestBlockedTurnLeavesFollowUpUnconsumed(t *testing.T) {
	q := &turn.InputQueue{}
	_ = q.Add(turn.Input{Kind: turn.FollowUp, Text: "next task"})
	task := activeTestTask()
	task.Status, task.Reason = "awaiting_input", "missing address"
	client := &stubClient{responses: []AssistantMessage{{Content: "received"}, {Content: "received"}, {Content: "received"}}}
	app := NewWithOptions(client, "test", Options{TaskStore: &memoryTaskStore{state: &task}})
	result, err := app.RunTurn(turn.WithInputQueue(context.Background(), q), []map[string]any{{"role": "user", "content": "continue"}}, 10)
	if err != nil || result.Outcome.Status != turn.Blocked || len(q.Close()) != 1 || len(client.messages) != 3 {
		t.Fatalf("result=%+v pending=%+v err=%v", result, q.Close(), err)
	}
}

func TestTurnBudgetLeavesFollowUpUnconsumed(t *testing.T) {
	q := &turn.InputQueue{}
	_ = q.Add(turn.Input{Kind: turn.FollowUp, Text: "next task"})
	app := NewWithOptions(&stubClient{responses: []AssistantMessage{{Content: "first task done"}}}, "test", Options{})
	result, err := app.RunTurn(turn.WithInputQueue(context.Background(), q), nil, 1)
	pending := q.Close()
	if err != nil || result.Reply != "first task done" || len(pending) != 1 || pending[0].Text != "next task" {
		t.Fatalf("result=%+v pending=%+v err=%v", result, pending, err)
	}
}

func TestTurnInputPersistenceFailureRestoresAcceptedInput(t *testing.T) {
	q := &turn.InputQueue{}
	_ = q.Add(turn.Input{ID: "first", Kind: turn.Steering, Text: "first correction"})
	_ = q.Add(turn.Input{ID: "second", Kind: turn.Steering, Text: "second correction"})
	app := NewWithOptions(&stubClient{}, "test", Options{Recorder: &failingRecorder{failAfter: 1}})
	ctx := turn.WithInputQueue(context.Background(), q)
	history, err := app.appendTurnInputs(ctx, nil, q.TakeSteering())
	pending := q.Close()
	if err == nil || len(history) != 1 || len(pending) != 1 || pending[0].ID != "second" {
		t.Fatalf("history=%+v pending=%+v err=%v", history, pending, err)
	}
}

func TestTurnCancellationReturnsUnconsumedInput(t *testing.T) {
	q := &turn.InputQueue{}
	_ = q.Add(turn.Input{Kind: turn.FollowUp, Text: "later task"})
	ctx, cancel := context.WithCancel(turn.WithInputQueue(context.Background(), q))
	cancel()
	app := NewWithOptions(&stubClient{}, "test", Options{})
	_, err := app.RunTurn(ctx, nil, 3)
	pending := q.Close()
	if !errors.Is(err, context.Canceled) || len(pending) != 1 || pending[0].Text != "later task" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}
