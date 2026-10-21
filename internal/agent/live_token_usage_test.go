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
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "lookup-1", Type: "function", Function: FunctionCall{Name: "lookup", Arguments: `{}`}}}, Usage: TokenUsage{PromptTokens: 100, CompletionTokens: 50}},
			{Role: "assistant", Content: "answer", Usage: TokenUsage{PromptTokens: 120, CompletionTokens: 8}},
		},
		chunks: [][]string{nil, {"answer"}},
	}
	app := NewWithOptions(stub, testModel, Options{Stream: true, Functions: map[string]tools.Function{
		"lookup": func(context.Context, map[string]any) (string, error) {
			if liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, OutputTokens: 50, Calls: 1}) {
				t.Errorf("usage before tool execution = %+v", latest)
			}
			return "found", nil
		},
	}})
	_, _, _, err := app.RunConversationTurnWithMetrics(ctx, []map[string]any{{"role": "user", "content": "search"}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 220, OutputTokens: 58, Calls: 2}) {
		t.Fatalf("whole-turn usage = %+v", latest)
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
	if liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, Calls: 1}) {
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
	if liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, OutputTokens: 12, Calls: 1}) {
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
			ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
			reader := &liveUsageEventReader{events: test.events, check: func(index int) {
				if index == 1 && liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, Calls: 1}) {
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
				if index == len(test.events) && liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, OutputTokens: 12, Calls: 1}) {
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
			if message.Content != "answer" || liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, OutputTokens: 12, Calls: 1}) {
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
				if liveTokenCounts(latest) != (LiveTokenUsage{InputTokens: 100, OutputTokens: 4, OutputEstimated: true, Calls: 1}) {
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

func TestLiveTokenUsageTracksActiveGenerationTime(t *testing.T) {
	var latest LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { latest = usage })
	collector := ctx.Value(liveUsageKey{}).(*liveUsageCollector)
	clock := time.Unix(100, 0)
	collector.now = func() time.Time { return clock }

	callCtx, finish := beginLiveTokenCall(ctx, nil, nil, ChatCompletionOptions{})
	if !latest.ActiveSince.Equal(clock) || latest.GenerationDuration != 0 {
		t.Fatalf("in-flight call = %+v", latest)
	}
	clock = clock.Add(2 * time.Second)
	reportLiveTokenDelta(callCtx, "abcd")
	if !latest.ActiveSince.Equal(time.Unix(100, 0)) || latest.OutputTokens != 1 {
		t.Fatalf("streaming snapshot = %+v", latest)
	}
	finish(AssistantMessage{Content: "abcd"})
	finish(AssistantMessage{Content: "abcd"})
	if !latest.ActiveSince.IsZero() || latest.GenerationDuration != 2*time.Second {
		t.Fatalf("finished call = %+v", latest)
	}

	// Tool execution between requests is not generation time.
	clock = clock.Add(10 * time.Second)
	_, finish = beginLiveTokenCall(ctx, nil, nil, ChatCompletionOptions{})
	if !latest.ActiveSince.Equal(clock) || latest.GenerationDuration != 2*time.Second {
		t.Fatalf("second call = %+v", latest)
	}
	clock = clock.Add(time.Second)
	finish(AssistantMessage{})
	if !latest.ActiveSince.IsZero() || latest.GenerationDuration != 3*time.Second || latest.Calls != 2 {
		t.Fatalf("whole turn = %+v", latest)
	}
}

// liveTokenCounts drops wall-clock timing so token assertions stay exact.
func liveTokenCounts(usage LiveTokenUsage) LiveTokenUsage {
	usage.GenerationDuration, usage.ActiveSince = 0, time.Time{}
	return usage
}
