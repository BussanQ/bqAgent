package agent

import (
	"bqagent/internal/tools"
	"bqagent/internal/turn"
	"context"
	"errors"
	"testing"
)

func TestCancelledToolBatchSkipsLaterMutations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &stubClient{responses: []AssistantMessage{{Role: "assistant", ToolCalls: []ToolCall{
		{ID: "slow", Type: "function", Function: FunctionCall{Name: "bash", Arguments: `{"command":"slow"}`}},
		{ID: "write", Type: "function", Function: FunctionCall{Name: "write_file", Arguments: `{"path":"file","content":"bad"}`}},
	}}}}
	writes := 0
	app := NewWithOptions(client, "test", Options{Functions: map[string]tools.Function{
		"bash":       func(context.Context, map[string]any) (string, error) { cancel(); return "", context.Canceled },
		"write_file": func(context.Context, map[string]any) (string, error) { writes++; return "written", nil },
	}})
	result, err := app.RunTurn(ctx, []map[string]any{{"role": "user", "content": "test"}}, 1)
	if !errors.Is(err, context.Canceled) || result.Outcome.Status != turn.Interrupted {
		t.Fatalf("outcome=%+v err=%v", result.Outcome, err)
	}
	if writes != 0 {
		t.Fatalf("cancelled batch performed %d writes", writes)
	}
	results := 0
	for _, msg := range result.Messages {
		if msg["role"] == "tool" {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("tool results=%d, want complete cancelled batch", results)
	}
}

type cancellationEventSink func(ToolEvent)

func (sink cancellationEventSink) EmitToolEvent(event ToolEvent) { sink(event) }

func TestRegularToolChecksCancellationAfterStartEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	app := NewWithOptions(&stubClient{}, "test", Options{
		Functions: map[string]tools.Function{"write_file": func(context.Context, map[string]any) (string, error) { calls++; return "", nil }},
		ToolEventSink: cancellationEventSink(func(event ToolEvent) {
			if event.Kind == "tool_start" {
				cancel()
			}
		}),
	})
	app.runRegularToolCall(ctx, ToolCall{ID: "write", Function: FunctionCall{Name: "write_file", Arguments: `{}`}})
	if calls != 0 {
		t.Fatal("tool executed after cancellation")
	}
}
