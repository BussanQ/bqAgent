package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func magpieSearchEvent(t *testing.T, event any) string {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(raw) + "\n\n"
}

func magpieSearchBlockEvents(t *testing.T, index int, block any) string {
	t.Helper()
	return magpieSearchEvent(t, map[string]any{"type": "content_block_start", "index": index, "content_block": block}) +
		magpieSearchEvent(t, map[string]any{"type": "content_block_stop", "index": index})
}

func magpieSearchStreamParts(t *testing.T) (string, string, string) {
	t.Helper()
	var fixture anthropicSearchResponse
	if err := json.Unmarshal([]byte(magpieMessagesSearchFixture), &fixture); err != nil {
		t.Fatal(err)
	}
	start := magpieSearchEvent(t, map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 20, "output_tokens": 2}}})
	// Magpie sends the search arguments as deltas, then a separate complete
	// result block. Match that wire format rather than a non-streaming block.
	call := magpieSearchEvent(t, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "server_tool_use", "id": "srvtoolu_search", "name": "web_search", "input": map[string]any{}}})
	for _, fragment := range []string{`{"query":"nerdctl `, `CDI support"}`} {
		call += magpieSearchEvent(t, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": fragment}})
	}
	call += magpieSearchEvent(t, map[string]any{"type": "content_block_stop", "index": 0})
	result := magpieSearchBlockEvents(t, 1, fixture.Content[1])
	return start, call, result
}

// A gateway whose search has finished but whose assistant turn never finishes.
// Reading beyond the supplied events deterministically reproduces a timeout,
// without sleeping, network access or an integration server.
type stalledMagpieSearchBody struct {
	io.Reader
	timedOut bool
	closed   bool
}

func (body *stalledMagpieSearchBody) Read(p []byte) (int, error) {
	n, err := body.Reader.Read(p)
	if err == io.EOF {
		body.timedOut = true
		return n, context.DeadlineExceeded
	}
	return n, err
}

func (body *stalledMagpieSearchBody) Close() error {
	body.closed = true
	return nil
}

func TestNativeWebSearchMagpieReturnsCompletedSourcesBeforeGatewayTimeout(t *testing.T) {
	start, call, result := magpieSearchStreamParts(t)
	// Do not pass speculative text preceding the search back as a summary.
	plan := magpieSearchBlockEvents(t, 2, map[string]any{"type": "text", "text": "UNVERIFIED_PLAN"})
	for _, apiType := range []APIType{APITypeAnthropic, APITypeOpenAI} {
		t.Run(string(apiType), func(t *testing.T) {
			body := &stalledMagpieSearchBody{Reader: strings.NewReader(start + plan + call + result)}
			var requestContext context.Context
			calls, fallbackCalls := 0, 0
			client := NewClientWithOptions("key", "http://magpie.example/v1", apiType, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				requestContext = request.Context()
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if request.URL.Path != "/v1/messages" || payload["stream"] != true || request.Header.Get("Accept") != "text/event-stream" || payload["tool_choice"] != nil {
					t.Fatalf("incorrect search request: %s %#v", request.URL, payload)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})}, ClientOptions{ProviderID: "magpie"})
			app := NewWithOptions(client, "bailian/deepseek", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
				fallbackCalls++
				return "API fallback", nil
			}}})
			var live LiveTokenUsage
			ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
			output, err := app.functions["web_search"](ctx, map[string]any{"query": "nerdctl CDI support"})
			if err != nil || !strings.Contains(output, "[Release notes](<https://example.com/releases>)") || strings.Contains(output, "UNVERIFIED_PLAN") {
				t.Fatalf("output=%q err=%v", output, err)
			}
			if calls != 1 || fallbackCalls != 0 || body.timedOut || !body.closed || requestContext.Err() != context.Canceled || ctx.Err() != nil {
				t.Fatalf("search did not stop promptly: calls=%d fallback=%d timeout=%v closed=%v request=%v parent=%v", calls, fallbackCalls, body.timedOut, body.closed, requestContext.Err(), ctx.Err())
			}
			if live.Calls != 1 || live.InputTokens != 20 || live.OutputTokens != 2 || client.apiType != apiType {
				t.Fatalf("reported usage or main protocol changed: %+v %s", live, client.apiType)
			}
		})
	}
}

func TestNativeWebSearchMagpieStreamRejectsUnverifiedResultsAndFallsBack(t *testing.T) {
	start, call, result := magpieSearchStreamParts(t)
	finish := magpieSearchEvent(t, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}) + magpieSearchEvent(t, map[string]any{"type": "message_stop"})
	resultWith := func(content any) string {
		return magpieSearchBlockEvents(t, 1, map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_search", "content": content})
	}
	unfinishedResult := magpieSearchEvent(t, map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "web_search_tool_result", "tool_use_id": "srvtoolu_search", "content": []any{map[string]any{"url": "https://example.com/releases"}}}})
	for name, events := range map[string]string{
		"no result":          call,
		"unfinished result":  call + unfinishedResult,
		"orphan result":      result,
		"mismatched call":    call + strings.ReplaceAll(result, "srvtoolu_search", "other_call"),
		"empty result":       call + resultWith([]any{}) + finish,
		"invalid source URL": call + resultWith([]any{map[string]any{"url": "javascript:alert(1)"}}) + finish,
		"ordinary links":     magpieSearchBlockEvents(t, 0, map[string]any{"type": "text", "text": "See https://example.com/releases"}) + finish,
		"server tool error":  call + resultWith(map[string]any{"type": "web_search_tool_result_error", "error_code": "unavailable"}),
		"stream error":       magpieSearchEvent(t, map[string]any{"type": "error", "error": map[string]any{"message": "search unavailable", "type": "api_error"}}),
		"malformed event":    "data: {invalid\n\n",
		"duplicate block":    call + call,
	} {
		t.Run(name, func(t *testing.T) {
			calls, fallbackCalls := 0, 0
			client := magpieMessagesTestClient(t, APITypeAnthropic, func(*http.Request, map[string]any) (int, string) {
				calls++
				return 200, start + events
			})
			app := NewWithOptions(client, "deepseek", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
				fallbackCalls++
				return "API fallback", nil
			}}})
			output, err := app.functions["web_search"](context.Background(), map[string]any{"query": "query"})
			if err != nil || output != "API fallback" || calls != 1 || fallbackCalls != 1 || !client.SupportsNativeWebSearch("deepseek") {
				t.Fatalf("output=%q calls=%d fallback=%d err=%v", output, calls, fallbackCalls, err)
			}
		})
	}
}

func TestNativeWebSearchMagpieStreamCancellationDiscardsRacingResult(t *testing.T) {
	start, call, result := magpieSearchStreamParts(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := magpieMessagesTestClient(t, APITypeAnthropic, func(*http.Request, map[string]any) (int, string) {
		calls++
		cancel()
		return 200, start + call + result
	})
	app := NewWithOptions(client, "deepseek", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
		t.Fatal("API fallback after cancellation")
		return "", nil
	}}})
	output, err := app.functions["web_search"](ctx, map[string]any{"query": "query"})
	if !errors.Is(err, context.Canceled) || output != "" || calls != 1 {
		t.Fatalf("output=%q calls=%d err=%v", output, calls, err)
	}
}

func TestNativeWebSearchMagpieStreamPausePreservesContentAndUsage(t *testing.T) {
	start, call, result := magpieSearchStreamParts(t)
	thinking := magpieSearchBlockEvents(t, 0, map[string]any{"type": "thinking", "thinking": "", "signature": "opaque-signature", "vendor": "opaque-field"})
	textStart := magpieSearchEvent(t, map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "text", "text": "Checking "}})
	textDelta := magpieSearchEvent(t, map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "text_delta", "text": "sources"}})
	textStop := magpieSearchEvent(t, map[string]any{"type": "content_block_stop", "index": 1})
	pause := magpieSearchEvent(t, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "pause_turn"}, "usage": map[string]any{"output_tokens": 5}}) + magpieSearchEvent(t, map[string]any{"type": "message_stop"})
	calls := 0
	client := magpieMessagesTestClient(t, APITypeAnthropic, func(_ *http.Request, body map[string]any) (int, string) {
		calls++
		if calls == 1 {
			return 200, start + thinking + textStart + textDelta + textStop + pause
		}
		messages := body["messages"].([]any)
		want := map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "opaque-signature", "vendor": "opaque-field"},
			map[string]any{"type": "text", "text": "Checking sources"},
		}}
		if len(messages) != 2 || !reflect.DeepEqual(messages[1], want) {
			t.Fatalf("continuation lost opaque content or text deltas: %#v", messages)
		}
		return 200, start + call + result
	})
	var live LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
	output, err := client.NativeWebSearch(ctx, "deepseek", "query")
	if err != nil || !strings.Contains(output, "https://example.com/releases") || strings.Contains(output, "Checking sources") || calls != 2 {
		t.Fatalf("output=%q calls=%d err=%v", output, calls, err)
	}
	if live.Calls != 2 || live.InputTokens != 40 || live.OutputTokens != 7 {
		t.Fatalf("partial message usage replaced input totals: %+v", live)
	}
}
