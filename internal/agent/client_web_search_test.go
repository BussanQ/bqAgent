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
	"time"

	"bqagent/internal/tools"
)

const responsesSearchFixture = `{"status":"completed","output":[{"type":"web_search_call","status":"completed","action":{"type":"search","sources":[{"type":"url","url":"https://example.com/news","title":"News"}]}},{"type":"message","content":[{"type":"output_text","text":"Verified news.","annotations":[{"type":"url_citation","url":"https://example.com/news","title":"News","start_index":0,"end_index":14}]}]}],"usage":{"input_tokens":20,"output_tokens":10}}`

func nativeSearchTestClient(t *testing.T, apiType APIType, respond func(*http.Request, map[string]any) (int, string)) *Client {
	t.Helper()
	return NewClientWithOptions("model-key", "https://relay.example/v1", apiType, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		status, data := respond(request, body)
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}, nil
	})}, ClientOptions{NativeWebSearch: "on"})
}

func TestNativeWebSearchCapability(t *testing.T) {
	for _, tc := range []struct {
		name, mode, endpoint, model, key string
		apiType                          APIType
		want                             bool
	}{
		{"responses", "", "", "gpt-5", "key", APITypeOpenAIResponse, true},
		{"anthropic", "auto", "", "claude-sonnet-4-5", "key", APITypeAnthropic, true},
		{"auto responses gateway", "auto", "https://relay.example/v1", "custom-model", "key", APITypeOpenAIResponse, true},
		{"auto anthropic gateway", "auto", "https://relay.example/v1", "custom-model", "key", APITypeAnthropic, true},
		{"magpie default", "", "http://127.0.0.1:3425/v1", "codex/gpt-6-astra", "", APITypeOpenAIResponse, true},
		{"magpie anthropic", "auto", "http://127.0.0.1:3425/v1", "bailian/deepseek", "", APITypeAnthropic, true},
		{"explicit gateway", "on", "https://relay.example/v1", "custom-model", "key", APITypeOpenAIResponse, true},
		{"off responses", "off", "", "gpt-5", "key", APITypeOpenAIResponse, false},
		{"off anthropic", "off", "", "claude-sonnet-4-5", "key", APITypeAnthropic, false},
		{"chat completions auto", "auto", "", "gpt-5", "key", APITypeOpenAI, false},
		{"chat completions on", "on", "", "gpt-5", "key", APITypeOpenAI, false},
		{"unknown model", "auto", "", "custom", "key", APITypeOpenAIResponse, true},
		{"no model credentials", "auto", "", "gpt-5", "", APITypeOpenAIResponse, true},
		{"empty model", "auto", "", "  ", "key", APITypeOpenAIResponse, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClientWithOptions(tc.key, tc.endpoint, tc.apiType, nil, ClientOptions{NativeWebSearch: tc.mode})
			if got := client.SupportsNativeWebSearch(tc.model); got != tc.want {
				t.Fatalf("SupportsNativeWebSearch = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNativeWebSearchResponsesRequestCitationsAndUsage(t *testing.T) {
	client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(request *http.Request, body map[string]any) (int, string) {
		if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer model-key" {
			t.Fatalf("incorrect endpoint/auth: %s", request.URL.Path)
		}
		if body["model"] != "current-model" || body["stream"] != true || body["max_tool_calls"] != float64(3) {
			t.Fatalf("request = %#v", body)
		}
		nativeTools := body["tools"].([]any)
		if len(nativeTools) != 1 || !reflect.DeepEqual(nativeTools[0], map[string]any{"type": "web_search"}) {
			t.Fatalf("expected only a server-side web_search tool: %#v", nativeTools)
		}
		if !reflect.DeepEqual(body["include"], []any{"web_search_call.action.sources"}) || !strings.Contains(body["input"].([]any)[1].(map[string]any)["content"].(string), "latest news") {
			t.Fatalf("missing query/sources: %#v", body)
		}
		return 200, responsesSearchFixture
	})
	var live LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
	ctx, collector := withTurnGenerationCollector(ctx)
	result, err := client.NativeWebSearch(ctx, "current-model", "latest news")
	if err != nil || !strings.Contains(result, "Verified news.") || !strings.Contains(result, "[News](<https://example.com/news>)") {
		t.Fatalf("result = %q, err = %v", result, err)
	}
	if strings.Count(result, "- [News]") != 1 || live.Calls != 1 || live.InputTokens != 20 || live.OutputTokens != 10 || live.InputEstimated || live.OutputEstimated {
		t.Fatalf("sources or usage incorrect: %s, %+v", result, live)
	}
	if collector.metricsFor("").CacheMetrics.Calls != 1 {
		t.Fatal("search usage omitted from turn metrics")
	}
}

func nativeSearchSSE(t *testing.T, terminalOutput bool) (string, string) {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal([]byte(responsesSearchFixture), &response); err != nil {
		t.Fatal(err)
	}
	event := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "data: " + string(data) + "\n\n"
	}
	var items string
	for index, item := range response["output"].([]any) {
		items += event(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	}
	if !terminalOutput {
		response["output"] = []any{}
	}
	return items, event(map[string]any{"type": "response.completed", "response": response})
}

func TestNativeWebSearchMagpieAutoWithoutSearchKeys(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	items, completed := nativeSearchSSE(t, false)
	for _, apiType := range []APIType{APITypeOpenAIResponse, APITypeAnthropic} {
		t.Run(string(apiType), func(t *testing.T) {
			calls := 0
			client := nativeSearchTestClient(t, apiType, func(request *http.Request, body map[string]any) (int, string) {
				calls++
				if request.URL.Host != "127.0.0.1:3425" || body["model"] != "codex/gpt-6-astra" {
					t.Fatalf("unexpected Magpie request: %s, %v", request.URL, body)
				}
				if apiType == APITypeOpenAIResponse {
					if body["stream"] != true || request.URL.Path != "/v1/responses" {
						t.Fatal("Magpie Responses search must request streaming to preserve search metadata")
					}
					return 200, items + completed
				}
				if request.URL.Path != "/v1/messages" {
					t.Fatal("Anthropic search must use messages")
				}
				return 200, `{"stop_reason":"end_turn","content":[{"type":"server_tool_use","name":"web_search"},{"type":"web_search_tool_result","content":[{"url":"https://example.com/news","title":"News"}]},{"type":"text","text":"Verified news."}]}`
			})
			client.nativeWebSearch = "auto"
			client.baseURL = "http://127.0.0.1:3425/v1"
			client.apiKey = ""
			app := NewWithOptions(client, "codex/gpt-6-astra", Options{})
			if !searchDefinitionPresent(app.toolDefinitionsForRun()) {
				t.Fatal("auto must expose search through Magpie without search API keys")
			}
			result, err := app.functions["web_search"](context.Background(), map[string]any{"query": "latest news"})
			if err != nil || calls != 1 || !strings.Contains(result, "Verified news.") || !strings.Contains(result, "https://example.com/news") {
				t.Fatalf("result=%q calls=%d err=%v", result, calls, err)
			}
		})
	}
}

func TestNativeWebSearchResponsesStreamPreservesSearchAndUsage(t *testing.T) {
	items, emptyCompleted := nativeSearchSSE(t, false)
	_, fullCompleted := nativeSearchSSE(t, true)
	for name, stream := range map[string]string{
		"Codex empty terminal output": items + emptyCompleted,
		"terminal output only":        fullCompleted,
		"duplicate terminal output":   items + fullCompleted,
		"terminal loses metadata":     items + strings.ReplaceAll(fullCompleted, `"annotations":[{"end_index":14,"start_index":0,"title":"News","type":"url_citation","url":"https://example.com/news"}]`, `"annotations":[]`),
		"last event without blank":    items + strings.TrimSpace(emptyCompleted),
		"multiline SSE and comments":  ": keepalive\n\nevent: response.completed\n" + strings.Replace(fullCompleted, "data: {", "data: {\ndata: ", 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) { return 200, stream })
			var live LiveTokenUsage
			ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
			ctx, collector := withTurnGenerationCollector(ctx)
			result, err := client.NativeWebSearch(ctx, "model", "query")
			if err != nil || strings.Count(result, "Verified news.") != 1 || strings.Count(result, "- [News]") != 1 {
				t.Fatalf("result=%q err=%v", result, err)
			}
			if live.Calls != 1 || live.InputTokens != 20 || live.OutputTokens != 10 || collector.metricsFor("").CacheMetrics.Calls != 1 {
				t.Fatalf("stream usage omitted or counted twice: %+v", live)
			}
		})
	}
}

func TestNativeWebSearchResponsesStreamRejectsIncompleteAndUnverified(t *testing.T) {
	items, completed := nativeSearchSSE(t, false)
	for name, stream := range map[string]string{
		"EOF after items":       items,
		"DONE without terminal": items + "data: [DONE]\n\n",
		"failed":                items + `data: {"type":"response.failed","response":{"error":{"message":"search failed"}}}` + "\n\n",
		"incomplete":            items + `data: {"type":"response.incomplete","response":{"status":"incomplete"}}` + "\n\n",
		"error":                 items + `data: {"type":"error","message":"gateway failure"}` + "\n\n",
		"malformed":             items + "data: {invalid}\n\n" + completed,
		"no search":             completed,
		"no sources":            strings.ReplaceAll(items, "https://example.com/news", "javascript:bad") + completed,
		"failed search item":    strings.Replace(items, `"status":"completed"`, `"status":"failed"`, 1) + completed,
		"inconsistent status":   items + strings.Replace(completed, `"status":"completed"`, `"status":"incomplete"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) { return 200, stream })
			result, err := client.NativeWebSearch(context.Background(), "model", "query")
			if err == nil || result != "" {
				t.Fatalf("accepted partial/unverified result=%q err=%v", result, err)
			}
			if !client.SupportsNativeWebSearch("model") {
				t.Fatal("stream failure must not be cached as unsupported")
			}
		})
	}
}

func TestNativeWebSearchRejectsUnverifiedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"ordinary answer":         `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"An answer from memory."}]}]}`,
		"citation without search": `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Answer","annotations":[{"url":"https://example.com"}]}]}]}`,
		"search without sources":  `{"status":"completed","output":[{"type":"web_search_call","status":"completed","action":{"type":"search"}}]}`,
		"failed search":           strings.Replace(responsesSearchFixture, `"status":"completed","action"`, `"status":"failed","action"`, 1),
		"truncated answer":        strings.Replace(responsesSearchFixture, `"status":"completed"`, `"status":"incomplete"`, 1),
		"unsafe source":           strings.ReplaceAll(responsesSearchFixture, "https://example.com/news", "javascript:alert(1)"),
	} {
		t.Run(name, func(t *testing.T) {
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) { return 200, body })
			if result, err := client.NativeWebSearch(context.Background(), "model", "query"); err == nil || result != "" {
				t.Fatalf("accepted non-search result = %q, err = %v", result, err)
			}
			if !client.SupportsNativeWebSearch("model") {
				t.Fatal("an unsuccessful query must not permanently disable native search")
			}
		})
	}
}

func TestNativeWebSearchAnthropicPausePreservesOpaqueContent(t *testing.T) {
	const content = `[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"news"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","url":"https://example.com/news","title":"News","encrypted_content":"opaque-secret","page_age":"today"}]}]`
	calls := 0
	client := nativeSearchTestClient(t, APITypeAnthropic, func(request *http.Request, body map[string]any) (int, string) {
		calls++
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "model-key" || request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatal("incorrect anthropic endpoint/auth/version")
		}
		if body["model"] != "claude-current" || !reflect.DeepEqual(body["tools"], []any{map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": float64(3)}}) {
			t.Fatalf("payload = %#v", body)
		}
		if calls == 1 {
			return 200, `{"stop_reason":"pause_turn","content":` + content + `,"usage":{"input_tokens":10,"output_tokens":5}}`
		}
		messages := body["messages"].([]any)
		var expected any
		if err := json.Unmarshal([]byte(content), &expected); err != nil {
			t.Fatal(err)
		}
		if len(messages) != 2 || !reflect.DeepEqual(messages[1], map[string]any{"role": "assistant", "content": expected}) {
			t.Fatalf("pause continuation lost/replaced opaque fields: %#v", messages)
		}
		if _, forced := body["tool_choice"]; forced {
			t.Fatal("continuation must allow a final summary")
		}
		return 200, `{"stop_reason":"end_turn","content":[{"type":"text","text":"Verified news.","citations":[{"type":"web_search_result_location","url":"https://example.com/news","title":"News","encrypted_index":"opaque-index","cited_text":"news"}]}],"usage":{"input_tokens":20,"output_tokens":8}}`
	})
	var live LiveTokenUsage
	ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
	result, err := client.NativeWebSearch(ctx, "claude-current", "query")
	if err != nil || calls != 2 || !strings.Contains(result, "[News](<https://example.com/news>)") || strings.Contains(result, "opaque-") {
		t.Fatalf("result = %q, calls = %d, err = %v", result, calls, err)
	}
	if live.Calls != 2 || live.InputTokens != 30 || live.OutputTokens != 13 {
		t.Fatalf("continuation usage = %+v", live)
	}
}

func TestNativeWebSearchAnthropicErrorsAndContinuationLimit(t *testing.T) {
	for name, body := range map[string]string{
		"server tool error": `{"stop_reason":"end_turn","content":[{"type":"web_search_tool_result","content":{"type":"web_search_tool_result_error","error_code":"unavailable"}}]}`,
		"pause loop":        `{"stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"x","name":"web_search","input":{"query":"news"}}]}`,
		"no search":         `{"stop_reason":"end_turn","content":[{"type":"text","text":"I know this already"}]}`,
		"truncated":         `{"stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := nativeSearchTestClient(t, APITypeAnthropic, func(*http.Request, map[string]any) (int, string) { calls++; return 200, body })
			if _, err := client.NativeWebSearch(context.Background(), "model", "query"); err == nil {
				t.Fatal("expected error")
			}
			want := 1
			if name == "pause loop" {
				want = 3
			}
			if calls != want || !client.SupportsNativeWebSearch("model") {
				t.Fatalf("calls = %d, want %d; transient failure disabled capability", calls, want)
			}
		})
	}
}

func TestNativeWebSearchCachesOnlyUnsupportedCapability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		message  string
		disabled bool
	}{
		{"unsupported", 400, "web_search is not supported by this model", true},
		{"disabled", 400, "web search is not enabled for your organization", true},
		{"unsupported anthropic", 422, "unknown tool web_search_20250305", true},
		{"bad arguments", 400, "web_search max_uses must be positive", false},
		{"rate limited", 429, "web_search not available", false},
		{"server error", 503, "web_search not available", false},
		{"auth", 401, "web_search is disabled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) { calls++; return tc.status, tc.message })
			for range 2 {
				_, _ = client.NativeWebSearch(context.Background(), "model", "query")
			}
			want := 2
			if tc.disabled {
				want = 1
			}
			if calls != want || client.SupportsNativeWebSearch("model") == tc.disabled {
				t.Fatalf("calls = %d, capability = %v", calls, client.SupportsNativeWebSearch("model"))
			}
			if !client.SupportsNativeWebSearch("other-model") {
				t.Fatal("unsupported capability leaked across models")
			}
			client.searchUnsupported.Store("model", time.Now().Add(-time.Second))
			if !client.SupportsNativeWebSearch("model") {
				t.Fatal("expired negative capability was not retried")
			}
		})
	}
}

func TestNativeWebSearchAgentFallbackAndAvailability(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	for _, withFallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "native only", true: "with API fallback"}[withFallback], func(t *testing.T) {
			calls, fallbackCalls := 0, 0
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) { calls++; return 400, "web_search is not supported" })
			options := Options{}
			if withFallback {
				options = Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(_ context.Context, args map[string]any) (string, error) {
					fallbackCalls++
					if args["query"] != "query" {
						t.Fatalf("args=%#v", args)
					}
					return "API result", nil
				}}}
			}
			app := NewWithOptions(client, "model", options)
			if !searchDefinitionPresent(app.toolDefinitionsForRun()) {
				t.Fatal("native search missing without search API key")
			}
			result, err := app.functions["web_search"](context.Background(), map[string]any{"search": "query"})
			if withFallback && (err != nil || result != "API result") {
				t.Fatalf("result=%q, err=%v", result, err)
			}
			if !withFallback && err == nil {
				t.Fatal("unsupported search must fail")
			}
			if got := searchDefinitionPresent(app.toolDefinitionsForRun()); got != withFallback {
				t.Fatalf("capability after rejection = %v", got)
			}
			_, _ = app.functions["web_search"](context.Background(), map[string]any{"query": "query"})
			if calls != 1 || (withFallback && fallbackCalls != 2) {
				t.Fatalf("native calls=%d fallback calls=%d", calls, fallbackCalls)
			}
			if searchDefinitionPresent(NewWithOptions(client, "model", Options{}).toolDefinitionsForRun()) {
				t.Fatal("cached unsupported model re-enabled in a new turn")
			}
			if !searchDefinitionPresent(NewWithOptions(client, "other-model", Options{}).toolDefinitionsForRun()) {
				t.Fatal("model switch failed to recompute capability")
			}
		})
	}
}

func searchDefinitionPresent(definitions []tools.Definition) bool {
	for _, definition := range definitions {
		if definition.Function.Name == "web_search" {
			return true
		}
	}
	return false
}

func TestNativeWebSearchCancellationAndInvalidQueryDoNotFallback(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	ctx, cancel := context.WithCancel(context.Background())
	client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) { cancel(); return 503, "cancelled" })
	fallbackCalls := 0
	app := NewWithOptions(client, "model", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) { fallbackCalls++; return "fallback", nil }}})
	if _, err := app.functions["web_search"](ctx, map[string]any{"query": "query"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if _, err := app.functions["web_search"](context.Background(), map[string]any{"query": " "}); err == nil {
		t.Fatal("empty query accepted")
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback called after cancellation or invalid query: %d", fallbackCalls)
	}
	if searchDefinitionPresent(NewWithOptions(client, "model", Options{ToolDefinitions: []tools.Definition{}, Functions: map[string]tools.Function{}}).toolDefinitionsForRun()) {
		t.Fatal("explicit restricted toolset unexpectedly gained search")
	}
}

func TestNativeWebSearchAgentLoopRefreshesToolAvailability(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "search success", true: "search unsupported"}[reject], func(t *testing.T) {
			mainCalls, searchCalls := 0, 0
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(_ *http.Request, body map[string]any) (int, string) {
				if reflect.DeepEqual(body["tools"], []any{map[string]any{"type": "web_search"}}) {
					searchCalls++
					if reject {
						return 400, "web_search is not supported with this model"
					}
					return 200, responsesSearchFixture
				}
				mainCalls++
				found := false
				for _, entry := range body["tools"].([]any) {
					if entry.(map[string]any)["name"] == "web_search" {
						found = true
					}
				}
				if found != (mainCalls == 1 || !reject) {
					t.Fatalf("request %d search=%v", mainCalls, found)
				}
				if mainCalls == 1 {
					return 200, `{"status":"completed","output":[{"type":"function_call","call_id":"search1","name":"web_search","arguments":"{\"query\":\"news\"}"}]}`
				}
				serialized, _ := json.Marshal(body["input"])
				want := "https://example.com/news"
				if reject {
					want = "no search API fallback configured"
				}
				if !strings.Contains(string(serialized), want) {
					t.Fatalf("search result not fed back to model: %s", serialized)
				}
				return 200, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`
			})
			app := NewWithOptions(client, "current-model", Options{APIType: APITypeOpenAIResponse, Context: ContextConfig{Enabled: false}})
			result, err := app.Run(context.Background(), "Search the news", 3)
			if err != nil || result != "done" || mainCalls != 2 || searchCalls != 1 {
				t.Fatalf("result=%q err=%v calls=%d/%d", result, err, mainCalls, searchCalls)
			}
		})
	}
}

func TestNativeWebSearchBackendPriorityAndTransientFallback(t *testing.T) {
	items, completed := nativeSearchSSE(t, false)
	for _, tc := range []struct {
		name         string
		apiType      APIType
		status       int
		body         string
		wantFallback bool
	}{
		{"native wins", APITypeOpenAIResponse, 200, responsesSearchFixture, false},
		{"stream native wins", APITypeOpenAIResponse, 200, items + completed, false},
		{"incomplete stream fallback", APITypeOpenAIResponse, 200, items, true},
		{"stream error fallback", APITypeOpenAIResponse, 200, items + `data: {"type":"error","message":"search unavailable"}` + "\n\n", true},
		{"rate limit fallback", APITypeOpenAIResponse, 429, "rate limited", true},
		{"server error fallback", APITypeAnthropic, 503, "unavailable", true},
		{"server tool error fallback", APITypeAnthropic, 200, `{"stop_reason":"end_turn","content":[{"type":"web_search_tool_result","content":{"type":"web_search_tool_result_error","error_code":"unavailable"}}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, fallbackCalls := 0, 0
			client := nativeSearchTestClient(t, tc.apiType, func(*http.Request, map[string]any) (int, string) { calls++; return tc.status, tc.body })
			app := NewWithOptions(client, "model", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) { fallbackCalls++; return "API result", nil }}})
			for range 2 {
				result, err := app.functions["web_search"](context.Background(), map[string]any{"query": "query"})
				if err != nil || (result == "API result") != tc.wantFallback {
					t.Fatalf("result=%q error=%v", result, err)
				}
			}
			wantFallback := 0
			if tc.wantFallback {
				wantFallback = 2
			}
			if calls != 2 || fallbackCalls != wantFallback || !searchDefinitionPresent(app.toolDefinitionsForRun()) {
				t.Fatalf("calls=%d fallback=%d; transient failure must retain search", calls, fallbackCalls)
			}
		})
	}
}
