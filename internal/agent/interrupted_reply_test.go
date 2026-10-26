package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
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

func TestRunTurnReplacesDraftOnProviderRetry(t *testing.T) {
	for _, failRetry := range []bool{false, true} {
		for _, useMessageSink := range []bool{false, true} {
			name := "success"
			if failRetry {
				name = "interrupted"
			}
			if useMessageSink {
				name += "/message_sink"
			} else {
				name += "/token_sink"
			}
			t.Run(name, func(t *testing.T) {
				calls := 0
				httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					body := providerStreamSuccess(APITypeOpenAI, "new answer")
					failure := "\ndata: {\"error\":{\"type\":\"server_error\",\"message\":\"connection closed\"}}\n"
					if calls == 1 {
						body = "data: {\"choices\":[{\"delta\":{\"content\":\"old draft\",\"tool_calls\":[{\"index\":0,\"id\":\"old_call\",\"function\":{\"name\":\"write_file\",\"arguments\":\"{\"}}]}}]}\n" + failure
					} else if failRetry {
						body = "data: {\"choices\":[{\"delta\":{\"content\":\"new answer\"}}]}\n" + failure
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				client := newTestResilientClient(APITypeOpenAI, "https://stream.test", httpClient, 0)
				var projection turn.Messages
				var tokens strings.Builder
				var drafts []string
				toolExecutions := 0
				options := Options{
					Stream: true, TokenSink: &tokens, Context: ContextConfig{Enabled: false},
					Functions: map[string]tools.Function{"write_file": func(context.Context, map[string]any) (string, error) {
						toolExecutions++
						return "written", nil
					}},
				}
				if useMessageSink {
					options.MessageSink = func(event turn.Event) {
						projection.Apply(event)
						if projection.Latest.Kind == "message.updated" && projection.Latest.Phase == "draft" {
							drafts = append(drafts, projection.Latest.Text)
						}
					}
				}
				app := NewWithOptions(client, "test", options)
				result, err := app.RunTurn(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, 1)
				if failRetry {
					if err == nil || result.Outcome.Status != turn.Failed {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				} else if err != nil || result.Reply != "new answer" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if calls != 2 || toolExecutions != 0 {
					t.Fatalf("calls=%d toolExecutions=%d", calls, toolExecutions)
				}
				last := result.Messages[len(result.Messages)-1]
				text := contentText(last["content"])
				if strings.Contains(text, "old draft") || !strings.HasSuffix(text, "new answer") || last["tool_calls"] != nil {
					t.Fatalf("last message=%#v", last)
				}
				if useMessageSink {
					if strings.Join(drafts, "|") != "old draft||new answer" || projection.Latest.Text != text {
						t.Fatalf("drafts=%q projection=%+v", drafts, projection)
					}
				} else if !strings.Contains(tokens.String(), "old draft\n\n[上游请求中断，正在重试；以下为重新生成的回复]\n\nnew answer") {
					t.Fatalf("tokens=%q", tokens.String())
				}
			})
		}
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
