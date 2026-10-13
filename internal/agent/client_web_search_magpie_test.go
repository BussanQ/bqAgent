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

// Same server-side blocks emitted by Magpie's renderAnthropic after its
// searcher has answered for a model that cannot search for itself.
const magpieMessagesSearchFixture = `{"stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srvtoolu_search","name":"web_search","input":{"query":"nerdctl CDI support"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_search","content":[{"type":"web_search_result","title":"Release notes","url":"https://example.com/releases","encrypted_content":"","page_age":null}]},{"type":"text","text":"Verified release notes."}],"usage":{"input_tokens":20,"output_tokens":10}}`

func magpieMessagesTestClient(t *testing.T, apiType APIType, respond func(*http.Request, map[string]any) (int, string)) *Client {
	t.Helper()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		status, data := respond(request, body)
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	})
	return NewClientWithOptions("gateway-key", "http://magpie.example:12345/prefix/v1", apiType, &http.Client{Transport: transport}, ClientOptions{ProviderID: "magpie"})
}

func TestNativeWebSearchMagpieMessagesDelegatesWithoutSearchKeys(t *testing.T) {
	t.Setenv("SEARCH_API_KEY", "")
	t.Setenv("FIRECRAWL_API_KEY", "")
	for _, apiType := range []APIType{APITypeOpenAIResponse, APITypeAnthropic, APITypeOpenAI} {
		for _, model := range []string{"workbuddy/deepseek-v4.1-flash", "another/provider-model"} {
			t.Run(string(apiType)+"/"+model, func(t *testing.T) {
				calls := 0
				client := magpieMessagesTestClient(t, apiType, func(request *http.Request, body map[string]any) (int, string) {
					calls++
					if request.URL.String() != "http://magpie.example:12345/prefix/v1/messages" || request.Header.Get("x-api-key") != "gateway-key" || request.Header.Get("anthropic-version") != "2023-06-01" {
						t.Fatalf("incorrect Magpie search endpoint or headers: %s", request.URL)
					}
					if body["model"] != model || body["stream"] != true || body["jsonrpc"] != nil {
						t.Fatalf("search must use Messages with the current model: %#v", body)
					}
					if !reflect.DeepEqual(body["tools"], []any{map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": float64(3)}}) {
						t.Fatalf("missing server search declaration: %#v", body)
					}
					if !reflect.DeepEqual(body["messages"], []any{map[string]any{"role": "user", "content": "nerdctl CDI support"}}) {
						t.Fatalf("search query not passed through: %#v", body)
					}
					return 200, magpieMessagesSearchFixture
				})
				// A model's rejection of native search must not hide Magpie's
				// provider search, which can use another provider for that model.
				client.searchUnsupported.Store(model, time.Now().Add(time.Hour))
				app := NewWithOptions(client, model, Options{})
				if !searchDefinitionPresent(app.toolDefinitionsForRun(false)) {
					t.Fatal("Magpie search missing without search API keys")
				}
				var live LiveTokenUsage
				ctx := WithLiveTokenUsage(context.Background(), func(usage LiveTokenUsage) { live = usage })
				result, err := app.functions["web_search"](ctx, map[string]any{"query": "nerdctl CDI support"})
				if err != nil || calls != 1 || !strings.Contains(result, "[Release notes](<https://example.com/releases>)") {
					t.Fatalf("result=%q calls=%d err=%v", result, calls, err)
				}
				if client.apiType != apiType || live.Calls != 1 || live.InputTokens != 20 || live.OutputTokens != 10 {
					t.Fatalf("chat protocol changed or usage lost: protocol=%s usage=%+v", client.apiType, live)
				}
			})
		}
	}
}

func TestNativeWebSearchMagpieMessagesFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		nativeSuccess bool
		apiType       APIType
	}{
		{"Messages unavailable falls back to Responses", 404, "not found", true, APITypeOpenAIResponse},
		{"proxy search failure falls back to API", 503, "search unavailable", false, APITypeOpenAIResponse},
		{"text links without search are rejected", 200, `{"stop_reason":"end_turn","content":[{"type":"text","text":"See https://example.com/releases"}]}`, false, APITypeOpenAIResponse},
		{"no sources falls back to API", 200, `{"stop_reason":"end_turn","content":[{"type":"server_tool_use","name":"web_search"},{"type":"web_search_tool_result","content":[]}]}`, false, APITypeOpenAIResponse},
		{"server search error falls back to API", 200, `{"stop_reason":"end_turn","content":[{"type":"web_search_tool_result","content":{"type":"web_search_tool_result_error","error_code":"unavailable"}}]}`, false, APITypeOpenAIResponse},
		{"Anthropic does not retry identical route", 503, "search unavailable", false, APITypeAnthropic},
		{"Chat skips unsupported native protocol", 503, "search unavailable", false, APITypeOpenAI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callerCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			callerDeadline, _ := callerCtx.Deadline()
			messagesCalls, nativeCalls, fallbackCalls := 0, 0, 0
			client := magpieMessagesTestClient(t, tc.apiType, func(request *http.Request, body map[string]any) (int, string) {
				current, ok := request.Context().Deadline()
				if !ok || !callerDeadline.Equal(current) {
					t.Fatal("fallback changed the caller's deadline")
				}
				switch request.URL.Path {
				case "/prefix/v1/messages":
					messagesCalls++
					return tc.status, tc.body
				case "/prefix/v1/responses":
					nativeCalls++
					if tc.nativeSuccess {
						return 200, responsesSearchFixture
					}
					return 400, "web_search is not supported"
				default:
					t.Fatalf("unexpected endpoint (must never call MCP): %s", request.URL)
					return 500, "unexpected"
				}
			})
			app := NewWithOptions(client, "deepseek", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
				fallbackCalls++
				return "API fallback", nil
			}}})
			result, err := app.functions["web_search"](callerCtx, map[string]any{"query": "query"})
			if err != nil || messagesCalls != 1 || (result == "API fallback") == tc.nativeSuccess {
				t.Fatalf("result=%q messages=%d native=%d fallback=%d err=%v", result, messagesCalls, nativeCalls, fallbackCalls, err)
			}
			wantNative := 0
			if tc.apiType == APITypeOpenAIResponse {
				wantNative = 1
			}
			wantFallback := 1
			if tc.nativeSuccess {
				wantFallback = 0
			}
			if nativeCalls != wantNative || fallbackCalls != wantFallback || !client.SupportsNativeWebSearch("deepseek") {
				t.Fatalf("incorrect fallback or search hidden: native=%d fallback=%d", nativeCalls, fallbackCalls)
			}
		})
	}
}

func TestNativeWebSearchMagpieMessagesCancellationDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := magpieMessagesTestClient(t, APITypeOpenAIResponse, func(request *http.Request, body map[string]any) (int, string) {
		calls++
		cancel()
		return 503, "cancelled"
	})
	app := NewWithOptions(client, "deepseek", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(context.Context, map[string]any) (string, error) {
		t.Fatal("API fallback after cancellation")
		return "", nil
	}}})
	_, err := app.functions["web_search"](ctx, map[string]any{"query": "query"})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestNativeWebSearchMagpieBridgeRequiresProviderIdentity(t *testing.T) {
	for _, provider := range []string{"", "other", "magpie"} {
		for _, mode := range []string{"auto", "off"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				client := NewClientWithOptions("", "http://localhost:3425/v1", APITypeOpenAI, nil, ClientOptions{ProviderID: provider, NativeWebSearch: mode})
				if client.SupportsNativeWebSearch("workbuddy/deepseek") != (provider == "magpie" && mode != "off") {
					t.Fatal("search capability did not respect provider identity or off mode")
				}
			})
		}
	}
	// The same model name and port on another provider must stay on its API.
	calls := 0
	client := nativeSearchTestClient(t, APITypeOpenAIResponse, func(request *http.Request, _ map[string]any) (int, string) {
		calls++
		if request.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected protocol switch: %s", request.URL)
		}
		return 200, responsesSearchFixture
	})
	client.baseURL = "http://localhost:3425/v1"
	if _, err := client.NativeWebSearch(context.Background(), "workbuddy/deepseek", "query"); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
