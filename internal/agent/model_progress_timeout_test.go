package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestStreamProgressTimeoutIgnoresHeartbeats(t *testing.T) {
	for _, initial := range []string{"", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"} {
		t.Run(initial, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				var body *nativeSearchTimedBody
				client := NewClientWithOptions("", "https://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					steps := []nativeSearchStreamStep{{data: initial}}
					for range 10 {
						steps = append(steps, nativeSearchStreamStep{20 * time.Second, ": heartbeat\n\ndata: {\"type\":\"response.in_progress\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"arguments\":\"\"}\n\n"})
					}
					body = &nativeSearchTimedBody{ctx: req.Context(), steps: steps}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				})}, ClientOptions{StreamIdleTimeout: time.Minute, StreamProgressTimeout: 2 * time.Minute})
				client.retryJitter = func(time.Duration) time.Duration { return 0 }
				start := time.Now()
				_, err := client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
				var providerErr *ProviderError
				if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorTimeout || !strings.Contains(err.Error(), "无有效进展") || errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
				if calls != 5 || time.Since(start) != 10*time.Minute+15*time.Second || !body.closed || providerErr.Request.RetryCount != 4 {
					t.Fatalf("calls=%d elapsed=%s closed=%v", calls, time.Since(start), body.closed)
				}
			})
		})
	}
}

func TestStreamProgressTimeoutRetriesWithFreshAttempt(t *testing.T) {
	for _, api := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		t.Run(string(api), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				calls := 0
				var bodies []*nativeSearchTimedBody
				var requests []string
				client := NewClientWithOptions("", "https://test.invalid", api, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					payload, _ := io.ReadAll(req.Body)
					requests = append(requests, string(payload))
					steps := []nativeSearchStreamStep{{30 * time.Second, providerStreamSuccess(api, "done")}}
					if calls == 1 {
						initial := `data: {"type":"response.in_progress"}`
						if api == APITypeAnthropic {
							initial = `data: {"type":"ping"}`
						} else if api == APITypeOpenAI {
							initial = `data: {"choices":[]}`
						}
						steps = []nativeSearchStreamStep{{data: initial + "\n\n"}}
						for range 10 {
							steps = append(steps, nativeSearchStreamStep{10 * time.Second, ": heartbeat\n\n"})
						}
					}
					body := &nativeSearchTimedBody{ctx: req.Context(), steps: steps}
					bodies = append(bodies, body)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				})}, ClientOptions{StreamIdleTimeout: 45 * time.Second, StreamProgressTimeout: time.Minute})
				client.retryJitter = func(time.Duration) time.Duration { return 0 }
				var logs, progress strings.Builder
				wrapped := instrumentChatCompletionClient(client, &logs, &progress)
				message, err := wrapped.(chatCompletionStreamOptionsClient).CreateChatCompletionStreamWithOptions(ctx, "test", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{}, nil)
				if err != nil || calls != 2 || message.FinalContent() != "done" || message.Request.RetryCount != 1 || ctx.Err() != nil {
					t.Fatalf("message=%+v err=%v calls=%d", message, err, calls)
				}
				if requests[0] != requests[1] || !bodies[0].closed || !bodies[1].closed {
					t.Fatal("retry changed the request or leaked a response body")
				}
				if !strings.Contains(logs.String(), `last_event="`+map[APIType]string{APITypeOpenAI: "chat.completion.chunk", APITypeOpenAIResponse: "response.in_progress", APITypeAnthropic: "ping"}[api]+`"`) {
					t.Fatalf("stream activity not recorded: %s", logs.String())
				}
				if !strings.Contains(progress.String(), "自动重试（第 1/4 次）") || !strings.Contains(logs.String(), "[ModelWait]") || !strings.Contains(logs.String(), "category=model_timeout") {
					t.Fatalf("progress=%s logs=%s", progress.String(), logs.String())
				}
			})
		})
	}
}

func TestStreamProgressTimeoutSharesRetryBudget(t *testing.T) {
	for _, timeoutFirst := range []bool{false, true} {
		t.Run(stringBool(timeoutFirst), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				client := NewClientWithOptions("", "https://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if (calls == 1) == timeoutFirst {
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &nativeSearchTimedBody{ctx: req.Context(), steps: []nativeSearchStreamStep{{2 * time.Minute, ""}}}}, nil
					}
					return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
				})}, ClientOptions{StreamProgressTimeout: time.Minute})
				client.retryJitter = func(time.Duration) time.Duration { return 0 }
				_, err := client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
				if err == nil || calls != 5 || ProviderRequestMetadataFromError(err).RetryCount != 4 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			})
		})
	}
}

func TestStreamProgressTimeoutCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		client := NewClientWithOptions("", "https://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &nativeSearchTimedBody{ctx: req.Context(), steps: []nativeSearchStreamStep{{2 * time.Minute, ""}}}}, nil
		})}, ClientOptions{StreamProgressTimeout: time.Minute})
		client.retrySleep = func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
		_, err := client.CreateChatCompletionStream(ctx, "test", nil, nil, nil)
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestStreamProgressTimeoutHonorsCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		calls := 0
		client := NewClientWithOptions("", "https://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &nativeSearchTimedBody{ctx: req.Context(), steps: []nativeSearchStreamStep{{2 * time.Minute, ""}}}}, nil
		})}, ClientOptions{StreamProgressTimeout: time.Minute})
		_, err := client.CreateChatCompletionStream(ctx, "test", nil, nil, nil)
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || ProviderRequestMetadataFromError(err).RetryCount != 0 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
}

func TestStreamProgressTimeoutRenewsOnModelOutput(t *testing.T) {
	for _, tc := range []struct {
		name            string
		api             APIType
		event, terminal string
	}{
		{"responses text", APITypeOpenAIResponse, `data: {"type":"response.output_text.delta","delta":"text"}`, `data: {"type":"response.completed","response":{"status":"completed"}}`},
		{"responses reasoning", APITypeOpenAIResponse, `data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`, `data: {"type":"response.completed","response":{"status":"completed"}}`},
		{"responses tool", APITypeOpenAIResponse, `data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":" "}`, `data: {"type":"response.completed","response":{"status":"completed"}}`},
		{"chat text", APITypeOpenAI, `data: {"choices":[{"delta":{"content":"text"}}]}`, `data: [DONE]`},
		{"chat reasoning", APITypeOpenAI, `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}`, `data: [DONE]`},
		{"chat tool", APITypeOpenAI, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" "}}]}}]}`, `data: [DONE]`},
		{"messages text", APITypeAnthropic, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"text"}}`, `data: {"type":"message_stop"}`},
		{"messages tool", APITypeAnthropic, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" "}}`, `data: {"type":"message_stop"}`},
		{"messages reasoning", APITypeAnthropic, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking"}}`, `data: {"type":"message_stop"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var steps []nativeSearchStreamStep
				wantProgress := 4
				if tc.name == "messages tool" {
					steps = append(steps, nativeSearchStreamStep{data: "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"read_file\"}}\n\n"})
					wantProgress++
				}
				for range 4 {
					steps = append(steps, nativeSearchStreamStep{45 * time.Second, tc.event + "\n\n"})
				}
				steps = append(steps, nativeSearchStreamStep{data: tc.terminal + "\n\n"})
				client := NewClientWithOptions("", "https://test.invalid", tc.api, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &nativeSearchTimedBody{ctx: req.Context(), steps: steps}}, nil
				})}, ClientOptions{StreamIdleTimeout: time.Minute, StreamProgressTimeout: time.Minute})
				progress := 0
				ctx := withModelProgressObserver(context.Background(), func() { progress++ })
				start := time.Now()
				_, err := client.CreateChatCompletionStream(ctx, "test", nil, nil, nil)
				if err != nil || time.Since(start) != 3*time.Minute || progress != wantProgress {
					t.Fatalf("error=%v elapsed=%s progress=%d", err, time.Since(start), progress)
				}
			})
		})
	}
}

func TestStreamProgressTimeoutDisabledAndCallerCancellation(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		t.Run(stringBool(cancelCaller), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				if cancelCaller {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					go func() { time.Sleep(10 * time.Second); cancel() }()
				}
				progressTimeout := time.Duration(0)
				if cancelCaller {
					progressTimeout = time.Minute
				}
				client := NewClientWithOptions("", "https://test.invalid", APITypeOpenAIResponse, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &nativeSearchTimedBody{ctx: req.Context(), steps: []nativeSearchStreamStep{{3 * time.Minute, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"}}}}, nil
				})}, ClientOptions{StreamProgressTimeout: progressTimeout})
				_, err := client.CreateChatCompletionStream(ctx, "test", nil, nil, nil)
				if cancelCaller && !errors.Is(err, context.Canceled) || !cancelCaller && err != nil {
					t.Fatalf("error=%v cancel=%v", err, cancelCaller)
				}
			})
		})
	}
}
