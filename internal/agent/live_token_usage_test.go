package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func TestLiveTokenUsageAccumulatesToolRounds(t *testing.T) {
	var latest LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
	stub := &generationMetricsStub{
		responses: []AssistantMessage{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "lookup-1", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{}`}}}, Usage: TokenUsage{PromptTokens: 100, CompletionTokens: 50, CachedPromptTokens: 40, CacheWritePromptTokens: 10, ReasoningTokens: 30}},
			{Role: "assistant", Content: "answer", Usage: TokenUsage{PromptTokens: 120, CompletionTokens: 8, CachedPromptTokens: 40, CacheWritePromptTokens: 10}},
		},
		chunks: [][]string{nil, {"answer"}},
	}
	app := NewWithOptions(stub, testModel, Options{Stream: true, Functions: map[string]tools.Function{
		"lookup": func(context.Context, map[string]any) (string, error) {
			if latest != (LiveTokenUsage{InputTokens: 100, OutputTokens: 50, Calls: 1, CachedInputTokens: 40, CacheWriteTokens: 10, CacheUsageAvailable: true, ReasoningTokens: 30}) {
				t.Errorf("usage before tool execution = %+v", latest)
			}
			return "found", nil
		},
	}})
	_, _, _, err := app.RunConversationTurnWithMetrics(ctx, []map[string]any{{"role": "user", "content": "search"}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if latest != (LiveTokenUsage{InputTokens: 220, OutputTokens: 58, Calls: 2, CachedInputTokens: 80, CacheWriteTokens: 20, CacheUsageAvailable: true, ReasoningTokens: 30}) {
		t.Fatalf("whole-turn usage = %+v", latest)
	}
}

func TestLiveTokenUsageCacheAndReasoningSnapshots(t *testing.T) {
	var latest LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
	ctx, finish := beginLiveTokenCall(ctx, nil, nil, ChatCompletionOptions{})
	reportLiveTokenUsage(ctx, TokenUsage{PromptTokens: 100, CompletionTokens: 50})
	// Breakdown fields may arrive only on the final message.
	finish(AssistantMessage{Usage: TokenUsage{CachedPromptTokens: 40, CacheWritePromptTokens: 10, ReasoningTokens: 20}})
	if latest.CachedInputTokens != 40 || latest.CacheWriteTokens != 10 || latest.ReasoningTokens != 20 || !latest.CacheUsageAvailable {
		t.Fatalf("final breakdown = %+v", latest)
	}
	// Cumulative snapshots replace earlier values; absent fields preserve them.
	for range 2 {
		reportLiveTokenUsage(ctx, TokenUsage{CachedPromptTokens: 60, CacheWritePromptTokens: 15, ReasoningTokens: 30})
	}
	reportLiveTokenUsage(ctx, TokenUsage{})
	finish(AssistantMessage{Usage: TokenUsage{CachedPromptTokens: 40, CacheWritePromptTokens: 10, ReasoningTokens: 20}})
	if latest.CachedInputTokens != 60 || latest.CacheWriteTokens != 15 || latest.ReasoningTokens != 30 {
		t.Fatalf("latest breakdown = %+v", latest)
	}
	reportLiveTokenUsage(ctx, TokenUsage{CacheUsageAvailable: true})
	if latest.CachedInputTokens != 0 || latest.CacheWriteTokens != 0 || !latest.CacheUsageAvailable {
		t.Fatalf("explicit cache zero = %+v", latest)
	}
}

func TestLiveTokenUsagePartialSnapshotsAndMissingUsage(t *testing.T) {
	var latest LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
	ctx, finish := beginLiveTokenCall(ctx, []map[string]any{{"role": "user", "content": "hello"}}, nil, ChatCompletionOptions{})
	if latest.InputTokens == 0 || !latest.InputEstimated || !latest.OutputEstimated {
		t.Fatalf("initial request estimate = %+v", latest)
	}
	start := TokenUsage{PromptTokens: 100, PromptTokensAvailable: true, CompletionTokensAvailable: true}
	reportLiveTokenUsage(ctx, start)
	if latest != (LiveTokenUsage{InputTokens: 100, Calls: 1}) {
		t.Fatalf("explicit zero = %+v", latest)
	}
	for _, text := range []string{"a", "b", "c", "d", "中", "文"} {
		reportLiveTokenDelta(ctx, text)
	}
	reportLiveTokenUsage(ctx, TokenUsage{})
	finish(AssistantMessage{Content: "abcd中文", Usage: start})
	if latest.OutputTokens != 3 || !latest.OutputEstimated || latest.InputEstimated {
		t.Fatalf("missing final usage must preserve estimate over stale start usage: %+v", latest)
	}
	reportLiveTokenUsage(ctx, TokenUsage{CompletionTokens: 12})
	reportLiveTokenUsage(ctx, TokenUsage{CompletionTokens: 12})
	if latest != (LiveTokenUsage{InputTokens: 100, OutputTokens: 12, Calls: 1}) {
		t.Fatalf("cumulative snapshots must replace, not add: %+v", latest)
	}
	reportLiveTokenDelta(ctx, "next")
	if latest.OutputTokens != 13 || !latest.OutputEstimated {
		t.Fatalf("estimate after last exact snapshot = %+v", latest)
	}
	reportLiveTokenUsage(ctx, TokenUsage{CompletionTokensAvailable: true})
	if latest.OutputTokens != 0 || latest.OutputEstimated {
		t.Fatalf("authoritative zero must replace estimate: %+v", latest)
	}
}

func TestLiveTokenUsageProvidersDistinguishZeroAndAbsent(t *testing.T) {
	for _, raw := range []string{`{}`, `{"prompt_tokens":0,"completion_tokens":0,"input_tokens":0,"output_tokens":0}`} {
		var chat chatCompletionUsage
		var responses openAIResponseUsage
		var anthropic anthropicUsage
		for _, value := range []any{&chat, &responses, &anthropic} {
			if err := json.Unmarshal([]byte(raw), value); err != nil {
				t.Fatal(err)
			}
		}
		for _, usage := range []TokenUsage{chat.tokenUsage(), tokenUsageFromOpenAIResponse(responses), anthropic.tokenUsage()} {
			want := raw != `{}`
			if usage.PromptTokensAvailable != want || usage.CompletionTokensAvailable != want {
				t.Fatalf("%s: availability = %+v", raw, usage)
			}
		}
	}
}

// Each read supplies one complete SSE event. Checking before the next read
// proves updates arrived while streaming, rather than only after EOF/return.
type liveUsageEventReader struct {
	events []string
	index  int
	check  func(int)
}

func (reader *liveUsageEventReader) Read(buffer []byte) (int, error) {
	reader.check(reader.index)
	if reader.index == len(reader.events) {
		return 0, io.EOF
	}
	event := "data: " + reader.events[reader.index] + "\n\n"
	if len(buffer) < len(event) {
		panic("test SSE event exceeds read buffer")
	}
	reader.index++
	return copy(buffer, event), nil
}

func TestLiveTokenUsageProviderStreamsUpdateBeforeCompletion(t *testing.T) {
	for _, test := range []struct {
		apiType APIType
		events  []string
	}{
		{APITypeOpenAI, []string{
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":0}}`,
			`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"content":"answer"},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":12}}`,
			`[DONE]`,
		}},
		{APITypeOpenAIResponse, []string{
			`{"type":"response.created","response":{"usage":{"input_tokens":100,"output_tokens":0}}}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call-1","name":"lookup"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`,
			`{"type":"response.output_text.delta","delta":"answer"}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":12}}}`,
		}},
		{APITypeAnthropic, []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":70,"cache_read_input_tokens":20,"cache_creation_input_tokens":10,"output_tokens":0}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"lookup","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
			`{"type":"message_stop"}`,
		}},
	} {
		t.Run(string(test.apiType), func(t *testing.T) {
			var latest LiveTokenUsage
			wantStart := LiveTokenUsage{InputTokens: 100, Calls: 1}
			if test.apiType == APITypeAnthropic {
				wantStart.CachedInputTokens = 20
				wantStart.CacheWriteTokens = 10
				wantStart.CacheUsageAvailable = true
			}
			wantFinal := wantStart
			wantFinal.OutputTokens = 12
			ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
			reader := &liveUsageEventReader{events: test.events, check: func(index int) {
				if index == 1 && latest != wantStart {
					t.Fatalf("input must be available at start, including cache exactly once: %+v", latest)
				}
				if index == 2 && (latest.OutputTokens != 2 || !latest.OutputEstimated) {
					t.Fatalf("reasoning must update before visible text: %+v", latest)
				}
				textEnd := len(test.events) - 2
				if test.apiType == APITypeOpenAIResponse {
					textEnd++
				}
				if index == textEnd && (latest.OutputTokens != 4 || !latest.OutputEstimated) {
					t.Fatalf("reasoning + arguments + text should be counted once: %+v", latest)
				}
				if index == len(test.events) && latest != wantFinal {
					t.Fatalf("final usage must arrive before EOF: %+v", latest)
				}
			}}
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(reader)}, nil
			})}
			client := instrumentGenerationMetrics(newTestResilientClient(test.apiType, "https://unused.test", httpClient, 0))
			message, err := client.CreateChatCompletionStream(ctx, testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if message.Content != "answer" || latest != wantFinal {
				t.Fatalf("message = %+v, usage = %+v", message, latest)
			}
		})
	}
}

func TestLiveTokenUsageStreamsWithoutFinalUsage(t *testing.T) {
	for _, test := range []struct {
		apiType  APIType
		events   []string
		terminal string
	}{
		{APITypeOpenAI, []string{
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":0}}`,
			`{"choices":[{"delta":{"content":"中文answer"}}]}`,
		}, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
		{APITypeOpenAIResponse, []string{
			`{"type":"response.created","response":{"usage":{"input_tokens":100,"output_tokens":0}}}`,
			`{"type":"response.output_text.delta","delta":"中文answer"}`,
		}, `{"type":"response.completed","response":{"status":"completed"}}`},
		{APITypeAnthropic, []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"中文answer"}}`,
		}, `{"type":"message_stop"}`},
	} {
		for _, truncated := range []bool{false, true} {
			name := string(test.apiType) + "/complete"
			if truncated {
				name = string(test.apiType) + "/truncated"
			}
			t.Run(name, func(t *testing.T) {
				body := "data: " + strings.Join(test.events, "\n\ndata: ") + "\n\n"
				if !truncated {
					body += "data: " + test.terminal + "\n\n"
					if test.apiType == APITypeOpenAI {
						body += "data: [DONE]\n\n"
					}
				}
				var latest LiveTokenUsage
				ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
				httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				client := instrumentGenerationMetrics(newTestResilientClient(test.apiType, "https://unused.test", httpClient, 0))
				_, err := client.CreateChatCompletionStream(ctx, testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, nil)
				if (err != nil) != truncated {
					t.Fatalf("truncated=%v, err=%v", truncated, err)
				}
				if latest != (LiveTokenUsage{InputTokens: 100, OutputTokens: 4, OutputEstimated: true, Calls: 1}) {
					t.Fatalf("missing final usage must retain streamed counts: %+v", latest)
				}
			})
		}
	}
}

func TestLiveTokenUsageNonStreamingAndNestedWrappers(t *testing.T) {
	for _, reported := range []bool{false, true} {
		var latest LiveTokenUsage
		ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
		stub := &generationMetricsStub{
			responses: []AssistantMessage{{Content: "中文answer", Usage: TokenUsage{PromptTokensAvailable: reported, CompletionTokensAvailable: reported}}},
			chunks:    [][]string{nil},
		}
		client := instrumentGenerationMetrics(instrumentGenerationMetrics(stub))
		_, err := client.CreateChatCompletion(ctx, testModel, []map[string]any{{"role": "user", "content": "hello"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if latest.Calls != 1 || latest.InputEstimated == reported || latest.OutputEstimated == reported {
			t.Fatalf("reported=%v: usage=%+v", reported, latest)
		}
		if reported && (latest.InputTokens != 0 || latest.OutputTokens != 0) {
			t.Fatalf("explicit zero: %+v", latest)
		}
		if !reported && (latest.InputTokens == 0 || latest.OutputTokens != 4) {
			t.Fatalf("missing usage: %+v", latest)
		}
	}
}

func TestLiveTokenUsageNestedStreamingCountsDeltasOnce(t *testing.T) {
	var latest LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
	stub := &generationMetricsStub{
		responses: []AssistantMessage{{Content: "中文answer"}},
		chunks:    [][]string{{"中", "文", "ans", "wer"}},
	}
	client := instrumentGenerationMetrics(instrumentGenerationMetrics(stub))
	_, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, func(string) {
		if latest.Calls != 1 || latest.OutputTokens > 4 {
			t.Fatalf("visible delta counted more than once: %+v", latest)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest.OutputTokens != 4 || !latest.OutputEstimated || latest.Calls != 1 {
		t.Fatalf("nested streaming usage = %+v", latest)
	}
}

// retryingStreamStub streams a failed attempt, signals a provider retry the
// way executeWithResilience does, then streams the replacement response.
type retryingStreamStub struct {
	before, after []string
	tick          func()
}

func (stub *retryingStreamStub) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (AssistantMessage, error) {
	return AssistantMessage{}, nil
}

func (stub *retryingStreamStub) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	for _, chunk := range stub.before {
		stub.tick()
		onChunk(chunk)
	}
	if observer, _ := ctx.Value(streamRetryObserverKey{}).(func()); observer != nil {
		observer()
	}
	for _, chunk := range stub.after {
		stub.tick()
		onChunk(chunk)
	}
	return AssistantMessage{Content: strings.Join(stub.after, "")}, nil
}

func TestLiveMetricsDiscardFailedStreamAttempt(t *testing.T) {
	var latest LiveTokenUsage
	var speeds []float64
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
	ctx = WithLiveSpeed(ctx, func(speed float64) { speeds = append(speeds, speed) })
	ctx, collector := withTurnGenerationCollector(ctx)
	clock := time.Unix(0, 0)
	stub := &retryingStreamStub{
		before: []string{"old ", "draft ", "text ", "here"},
		after:  []string{"new"},
	}
	stub.tick = func() { clock = clock.Add(time.Second) }
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return clock }}
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Only the retried response ("new" = 3 ASCII units -> 1 token) is counted.
	if latest.OutputTokens != 1 || latest.Calls != 1 {
		t.Fatalf("usage = %+v", latest)
	}
	// The retry retracts the failed attempt's live speed.
	if len(speeds) == 0 || speeds[len(speeds)-1] != 0 {
		t.Fatalf("speeds = %v", speeds)
	}
	// TTFT is measured from the retry, not from the failed attempt.
	if metrics := collector.metricsFor("new"); !metrics.Available || metrics.FirstTokenLatency != time.Second {
		t.Fatalf("metrics = %+v", metrics)
	}
}
