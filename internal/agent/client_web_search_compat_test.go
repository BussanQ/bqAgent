package agent

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
)

func TestNativeWebSearchThinkingToolChoiceCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name                string
		apiType             APIType
		magpie              bool
		messagesUnavailable bool
		wantPaths           []string
	}{
		{"Responses", APITypeOpenAIResponse, false, false, []string{"/v1/responses"}},
		{"Anthropic", APITypeAnthropic, false, false, []string{"/v1/messages"}},
		{"Magpie Responses", APITypeOpenAIResponse, true, false, []string{"/prefix/v1/messages"}},
		{"Magpie Anthropic", APITypeAnthropic, true, false, []string{"/prefix/v1/messages"}},
		{"Magpie Chat", APITypeOpenAI, true, false, []string{"/prefix/v1/messages"}},
		{"Magpie Responses fallback", APITypeOpenAIResponse, true, true, []string{"/prefix/v1/messages", "/prefix/v1/responses"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			respond := func(request *http.Request, body map[string]any) (int, string) {
				paths = append(paths, request.URL.Path)
				if tc.messagesUnavailable && strings.HasSuffix(request.URL.Path, "/messages") {
					return http.StatusNotFound, "Messages unavailable"
				}
				// Reproduce Bailian's upstream 400 wrapped in Magpie's HTTP 502.
				// Neither the primary route nor its fallback may force a tool.
				_, objectChoice := body["tool_choice"].(map[string]any)
				if body["tool_choice"] == "required" || objectChoice {
					return http.StatusBadGateway, "Bailian: <400> InternalError.Algo.InvalidParameter: The tool_choice parameter does not support being set to required or object in thinking mode"
				}
				if body["thinking"] != nil || body["reasoning"] != nil {
					t.Fatal("search compatibility must not override provider thinking settings")
				}
				if strings.HasSuffix(request.URL.Path, "/messages") {
					return http.StatusOK, magpieMessagesSearchFixture
				}
				return http.StatusOK, responsesSearchFixture
			}
			client := nativeSearchTestClient(t, tc.apiType, respond)
			if tc.magpie {
				client = magpieMessagesTestClient(t, tc.apiType, respond)
			}
			result, err := client.NativeWebSearch(context.Background(), "bailian/qwen3.8-flash", "nerdctl CDI support")
			if err != nil || !strings.Contains(result, "Sources:") || !reflect.DeepEqual(paths, tc.wantPaths) {
				t.Fatalf("result=%q paths=%v err=%v", result, paths, err)
			}
			if !client.SupportsNativeWebSearch("bailian/qwen3.8-flash") {
				t.Fatal("thinking compatibility disabled search")
			}
		})
	}
}

func TestNativeWebSearchResponsesUnsupportedParameterCompatibility(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	items, completed := nativeSearchSSE(t, false)
	for _, rejected := range [][]string{{"max_tool_calls"}, {"max_output_tokens"}, {"max_tool_calls", "max_output_tokens"}} {
		t.Run(strings.Join(rejected, "+"), func(t *testing.T) {
			callerCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			callerDeadline, _ := callerCtx.Deadline()
			calls := 0
			checkInitialLimits := true
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(request *http.Request, body map[string]any) (int, string) {
				calls++
				deadline, ok := request.Context().Deadline()
				if !ok || !deadline.Equal(callerDeadline) {
					t.Fatal("compatibility retries must preserve the caller's deadline")
				}
				if body["stream"] != true || !reflect.DeepEqual(body["tools"], []any{map[string]any{"type": "web_search"}}) || !reflect.DeepEqual(body["include"], []any{"web_search_call.action.sources"}) {
					t.Fatalf("compatibility retry dropped search requirements: %#v", body)
				}
				if checkInitialLimits {
					if body["max_tool_calls"] != float64(3) || body["max_output_tokens"] != float64(4096) {
						t.Fatal("parameter cache leaked across models or outlived its TTL")
					}
					checkInitialLimits = false
				}
				if len(rejected) == 1 {
					preserved, value := "max_tool_calls", float64(3)
					if rejected[0] == preserved {
						preserved, value = "max_output_tokens", 4096
					}
					if body[preserved] != value {
						t.Fatalf("unrelated parameter %s was dropped", preserved)
					}
				}
				for _, parameter := range rejected {
					if _, present := body[parameter]; present {
						if parameter == "max_output_tokens" {
							return 422, `{"error":{"message":"Parameter 'max_output_tokens' is not supported."}}`
						}
						// The exact error reported by Codex through Magpie.
						return 400, `{"error":{"message":"Codex: Unsupported parameter: max_tool_calls"}}`
					}
				}
				return 200, items + completed
			})
			client.nativeWebSearch = "auto"
			client.baseURL = "http://127.0.0.1:3425/v1"
			app := NewWithOptions(client, "codex/gpt-6-astra", Options{})
			var live LiveTokenUsage
			ctx := WithLiveTokenUsage(callerCtx, func(usage LiveTokenUsage) { live = usage })
			ctx, collector := withTurnGenerationCollector(ctx)
			result, err := app.functions["web_search"](ctx, map[string]any{"query": "nerdctl CDI support"})
			if err != nil || calls != len(rejected)+1 || !strings.Contains(result, "[News](<https://example.com/news>)") {
				t.Fatalf("Magpie search result=%q calls=%d err=%v", result, calls, err)
			}
			if live.Calls != 1 || live.InputTokens != 20 || live.OutputTokens != 10 || collector.metricsFor("").CacheMetrics.Calls != 1 {
				t.Fatalf("rejected requests inflated successful search usage: %+v", live)
			}
			if !searchDefinitionPresent(app.toolDefinitionsForRun(false)) {
				t.Fatal("optional parameter rejection disabled the search tool")
			}
			if _, err := client.NativeWebSearch(callerCtx, "codex/gpt-6-astra", "another query"); err != nil || calls != len(rejected)+2 {
				t.Fatalf("cached compatibility still retried: calls=%d err=%v", calls, err)
			}
			checkInitialLimits = true
			if _, err := client.NativeWebSearch(callerCtx, "other-model", "query"); err != nil || calls != 2*len(rejected)+3 {
				t.Fatalf("model isolation failed: calls=%d err=%v", calls, err)
			}
			for _, parameter := range rejected {
				client.searchUnsupportedParameters.Store(nativeSearchParameterKey{model: "codex/gpt-6-astra", parameter: parameter}, time.Now().Add(-time.Second))
			}
			checkInitialLimits = true
			if _, err := client.NativeWebSearch(callerCtx, "codex/gpt-6-astra", "query"); err != nil || calls != 3*len(rejected)+4 {
				t.Fatalf("expired compatibility not retried: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestNativeWebSearchResponsesParameterCompatibilityFailures(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		status, calls int
	}{
		{"repeated rejection is bounded", "web_search: Codex: Unsupported parameter: max_tool_calls", 400, 2},
		{"invalid value", "max_tool_calls must be positive", 400, 1},
		{"other field", "Unsupported parameter: max_tool_calls_extra", 400, 1},
		{"nested field", "Unsupported parameter: tools.max_tool_calls", 400, 1},
		{"required field", "Unsupported parameter: include", 400, 1},
		{"auth", "Unsupported parameter: max_tool_calls", 401, 1},
		{"rate limit", "Unsupported parameter: max_tool_calls", 429, 1},
		{"server failure", "Unsupported parameter: max_tool_calls", 503, 1},
		{"accepted stream error", "data: {\"type\":\"error\",\"message\":\"Unsupported parameter: max_tool_calls\"}\n\n", 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, fallbackCalls := 0, 0
			client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) {
				calls++
				return tc.status, tc.message
			})
			app := NewWithOptions(client, "model", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
				fallbackCalls++
				return "API result", nil
			}}})
			result, err := app.functions["web_search"](context.Background(), map[string]any{"query": "query"})
			if err != nil || result != "API result" || calls != tc.calls || fallbackCalls != 1 {
				t.Fatalf("result=%q calls=%d fallback=%d err=%v", result, calls, fallbackCalls, err)
			}
			if !client.SupportsNativeWebSearch("model") {
				t.Fatal("request rejection disabled native search capability")
			}
		})
	}
}

func TestNativeWebSearchResponsesParameterCompatibilityCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls, fallbackCalls := 0, 0
	client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(*http.Request, map[string]any) (int, string) {
		calls++
		cancel()
		return 400, "Codex: Unsupported parameter: max_tool_calls"
	})
	app := NewWithOptions(client, "model", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
		fallbackCalls++
		return "API result", nil
	}}})
	_, err := app.functions["web_search"](ctx, map[string]any{"query": "query"})
	if !errors.Is(err, context.Canceled) || calls != 1 || fallbackCalls != 0 {
		t.Fatalf("cancelled compatibility request: calls=%d fallback=%d err=%v", calls, fallbackCalls, err)
	}
}
