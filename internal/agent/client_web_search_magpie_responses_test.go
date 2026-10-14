package agent

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestNativeWebSearchMagpieResponsesUsesConfiguredProtocol(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	items, completed := nativeSearchSSE(t, false)
	for name, reply := range map[string]string{"JSON": responsesSearchFixture, "SSE empty terminal output": items + completed} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := magpieMessagesTestClient(t, APITypeOpenAIResponse, func(request *http.Request, body map[string]any) (int, string) {
				calls++
				if request.URL.String() != "http://magpie.example:12345/prefix/v1/responses" || request.Header.Get("Authorization") != "Bearer gateway-key" {
					t.Fatalf("incorrect endpoint or credentials: %s", request.URL)
				}
				if body["model"] != "workbuddy/deepseek" || body["stream"] != true || body["messages"] != nil || body["tool_choice"] != nil {
					t.Fatalf("incorrect Responses request: %#v", body)
				}
				if !reflect.DeepEqual(body["tools"], []any{map[string]any{"type": "web_search"}}) || body["input"].([]any)[1].(map[string]any)["content"] != "latest news" {
					t.Fatalf("missing search tool or query: %#v", body)
				}
				return http.StatusOK, reply
			})
			app := NewWithOptions(client, "workbuddy/deepseek", Options{})
			if !searchDefinitionPresent(app.toolDefinitionsForRun(false)) {
				t.Fatal("Magpie Responses search missing without search API keys")
			}
			var live LiveTokenUsage
			ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
			output, err := app.functions["web_search"](ctx, map[string]any{"query": "latest news"})
			if err != nil || calls != 1 || !strings.Contains(output, "[News](<https://example.com/news>)") || live.Calls != 1 || live.InputTokens != 20 || live.OutputTokens != 10 {
				t.Fatalf("output=%q calls=%d usage=%+v err=%v", output, calls, live, err)
			}
		})
	}
}

func TestNativeWebSearchMagpieResponsesFailureNeverUsesMessages(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		body        string
		unsupported bool
	}{
		{"unsupported", 400, "web_search is not supported", true},
		{"unavailable endpoint", 404, "not found", false},
		{"transient failure", 503, "search unavailable", false},
		{"unverified answer", 200, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"See https://example.com/news"}]}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, fallbackCalls := 0, 0
			client := magpieMessagesTestClient(t, APITypeOpenAIResponse, func(request *http.Request, _ map[string]any) (int, string) {
				calls++
				if request.URL.Path != "/prefix/v1/responses" {
					t.Fatalf("unexpected cross-protocol request: %s", request.URL)
				}
				return tc.status, tc.body
			})
			app := NewWithOptions(client, "model", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
				fallbackCalls++
				return "API fallback", nil
			}}})
			for range 2 {
				output, err := app.functions["web_search"](context.Background(), map[string]any{"query": "query"})
				if err != nil || output != "API fallback" {
					t.Fatalf("output=%q err=%v", output, err)
				}
			}
			wantCalls := 2
			if tc.unsupported {
				wantCalls = 1
			}
			if calls != wantCalls || fallbackCalls != 2 || client.SupportsNativeWebSearch("model") == tc.unsupported || !client.SupportsNativeWebSearch("other-model") {
				t.Fatalf("calls=%d fallback=%d supported=%v", calls, fallbackCalls, client.SupportsNativeWebSearch("model"))
			}
			withoutFallback := NewWithOptions(client, "model", Options{})
			if searchDefinitionPresent(withoutFallback.toolDefinitionsForRun(false)) == tc.unsupported {
				t.Fatal("search availability did not honor the Responses capability cache")
			}
		})
	}
}
