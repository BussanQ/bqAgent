package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

type interruptedReplyClient struct {
	stubClient
	chunks []string
	err    error
	cancel context.CancelFunc
}

func (c *interruptedReplyClient) CreateChatCompletionStreamWithOptions(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ ChatCompletionOptions, onChunk func(string)) (AssistantMessage, error) {
	for _, chunk := range c.chunks {
		onChunk(chunk)
	}
	if c.cancel != nil {
		c.cancel()
	}
	return AssistantMessage{ToolCalls: []ToolCall{{ID: "unfinished", Function: FunctionCall{Name: "write_file", Arguments: `{`}}}}, c.err
}

func TestRunTurnPreservesInterruptedReply(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		cancel bool
		chunks []string
	}{
		{"provider failure", errors.New("connection lost"), false, []string{"天空", "是蓝色的"}},
		{"cancellation", context.Canceled, true, []string{"天空", "是蓝色的"}},
		{"cancel after response", nil, true, []string{"天空是蓝色的"}},
		{"no text", errors.New("connection lost"), false, nil},
		{"whitespace", errors.New("connection lost"), false, []string{" \n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &interruptedReplyClient{chunks: tc.chunks, err: tc.err}
			if tc.cancel {
				client.cancel = cancel
			}
			recorder := &capturingRecorder{}
			var projection turn.Messages
			app := NewWithOptions(client, "test", Options{Stream: true, Recorder: recorder, MessageSink: func(e turn.Event) { projection.Apply(e) }, Context: ContextConfig{Enabled: false}})
			history := []map[string]any{{"role": "user", "content": "天空为什么蓝色"}}
			result, err := app.RunTurn(ctx, history, 1)
			wantErr, wantStatus := tc.err, turn.Failed
			if tc.cancel {
				wantErr, wantStatus = context.Canceled, turn.Interrupted
			}
			if !errors.Is(err, wantErr) || result.Outcome.Status != wantStatus || result.Reply != "" {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if len(recorder.recorded) == 0 || !isTaskContextMessage(recorder.recorded[0]) || len(result.Messages) < 3 || !isTaskContextMessage(result.Messages[2]) {
				t.Fatal("sent task context must survive an interrupted request")
			}
			if strings.TrimSpace(strings.Join(tc.chunks, "")) == "" {
				if len(recorder.recorded) != 1 || len(result.Messages) != 3 {
					t.Fatalf("empty draft saved: %+v", result.Messages)
				}
				return
			}
			if len(result.Messages) != 4 || len(recorder.recorded) != 2 {
				t.Fatalf("history=%+v recorded=%+v", result.Messages, recorder.recorded)
			}
			draft := result.Messages[len(result.Messages)-1]
			text := contentText(draft["content"])
			if !strings.Contains(text, "未完成的回复") || !strings.HasSuffix(text, "天空是蓝色的") || draft["phase"] == "final_answer" || draft["tool_calls"] != nil || draft["response_items"] != nil {
				t.Fatalf("invalid interrupted draft: %+v", draft)
			}
			if projection.Latest.Text != text || projection.Latest.Phase != "interrupted" || projection.Latest.Kind == "message.completed" {
				t.Fatalf("projection=%+v", projection)
			}
			if len(history) != 1 || history[0]["content"] != "天空为什么蓝色" {
				t.Fatal("input history mutated")
			}
		})
	}
}

func TestRunTurnReportsInterruptedReplySaveFailure(t *testing.T) {
	providerErr := errors.New("connection lost")
	app := NewWithOptions(&interruptedReplyClient{chunks: []string{"partial"}, err: providerErr}, "test", Options{Stream: true, Recorder: &failingRecorder{failAfter: 1}})
	result, err := app.RunTurn(context.Background(), nil, 1)
	if !errors.Is(err, providerErr) || !strings.Contains(err.Error(), "save interrupted reply") || result.Outcome.Status != turn.Failed {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
