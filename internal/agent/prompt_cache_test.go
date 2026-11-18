package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestPromptSnapshotPlacesModelIdentityBeforeSessionMemory(t *testing.T) {
	first := NewPromptSnapshot("stable instructions", "# Memory\nremember this", "gpt-5.6-sol", APITypeOpenAIResponse)
	second := NewPromptSnapshot("stable instructions", "# Memory\nremember this", "gpt-5.7", APITypeOpenAIResponse)
	if !strings.Contains(first.Stable, "Current runtime model: gpt-5.6-sol") {
		t.Fatalf("stable prompt = %q, want model identity", first.Stable)
	}
	if strings.Contains(first.SessionContext, "Current runtime model:") {
		t.Fatalf("session context contains model identity: %q", first.SessionContext)
	}
	if strings.Index(first.Combined(), "Current runtime model:") > strings.Index(first.Combined(), "# Memory") {
		t.Fatalf("combined prompt has model identity after memory: %q", first.Combined())
	}
	if first.StableHash == second.StableHash {
		t.Fatal("model change did not change stable hash")
	}
}

func TestPromptCacheKeyIncludesDeterministicRequestShape(t *testing.T) {
	prompt := NewPromptSnapshot("stable", "memory", "gpt-5.6", APITypeOpenAIResponse)
	firstDefinitions := []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "first"}}}
	secondDefinitions := []tools.Definition{{Type: "function", Function: tools.FunctionDefinition{Name: "second"}}}
	first := buildPromptCacheOptions(APITypeOpenAIResponse, "gpt-5.6", prompt, firstDefinitions, ChatCompletionOptions{})
	repeated := buildPromptCacheOptions(APITypeOpenAIResponse, "gpt-5.6", prompt, firstDefinitions, ChatCompletionOptions{})
	changed := buildPromptCacheOptions(APITypeOpenAIResponse, "gpt-5.6", prompt, secondDefinitions, ChatCompletionOptions{})
	if first.Key != repeated.Key || first.RequestShapeHash != repeated.RequestShapeHash {
		t.Fatalf("cache shape is not deterministic: %#v != %#v", first, repeated)
	}
	if first.Key == changed.Key {
		t.Fatal("different tool sets produced the same cache key")
	}
	if len(first.Key) > 64 || !strings.HasPrefix(first.Key, "bq1:") {
		t.Fatalf("cache key = %q, want bq1 key no longer than 64 bytes", first.Key)
	}
	hexNamedModelKey := promptCacheKey("deadbeef", prompt.StableHash, first.RequestShapeHash)
	if strings.Contains(hexNamedModelKey, ":deadbeef:") {
		t.Fatalf("cache key leaked a hex-shaped model name instead of hashing it: %q", hexNamedModelKey)
	}
}

func TestProviderPromptCacheRequestShapes(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "stable instructions"},
		{"role": "system", "content": "session memory"},
		{"role": "user", "content": "hello"},
	}
	definitions := tools.Definitions()[:2]
	prompt := NewFrozenPromptSnapshot("stable instructions", "session memory")

	t.Run("anthropic", func(t *testing.T) {
		options := ChatCompletionOptions{}
		options.PromptCache = buildPromptCacheOptions(APITypeAnthropic, "claude-sonnet", prompt, definitions, options)
		request, err := buildAnthropicRequest("claude-sonnet", messages, definitions, options, false)
		if err != nil {
			t.Fatal(err)
		}
		if request.CacheControl == nil || request.CacheControl.Type != "ephemeral" {
			t.Fatalf("top-level cache control = %#v", request.CacheControl)
		}
		if len(request.System) != 2 || request.System[0].CacheControl == nil || request.System[1].CacheControl != nil {
			t.Fatalf("system blocks = %#v", request.System)
		}
		if len(request.Tools) == 0 || request.Tools[len(request.Tools)-1].CacheControl == nil {
			t.Fatalf("tools = %#v, want breakpoint on last tool", request.Tools)
		}
	})
}

func TestOpenAIPromptCacheKeyOnly(t *testing.T) {
	const model = "gpt-5.6"
	messages := []map[string]any{{"role": "system", "content": "stable"}, {"role": "user", "content": "hello"}}
	for _, api := range []APIType{APITypeOpenAI, APITypeOpenAIResponse} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", api, stream), func(t *testing.T) {
				options := ChatCompletionOptions{}
				options.PromptCache = buildPromptCacheOptions(api, model, NewFrozenPromptSnapshot("stable", ""), nil, options)
				client := NewClientWithAPIType("", "https://cache.test/v1", api, &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					var fields struct {
						Key string `json:"prompt_cache_key"`
					}
					if err := json.Unmarshal(body, &fields); err != nil {
						return nil, err
					}
					if fields.Key == "" || fields.Key != options.PromptCache.Key || strings.Contains(string(body), `"prompt_cache_breakpoint"`) || strings.Contains(string(body), `"prompt_cache_options"`) {
						t.Fatalf("unexpected prompt cache fields in request: %s", body)
					}
					reply := providerJSONSuccess(api, "done")
					if stream {
						reply = providerStreamSuccess(api, "done")
					} else if api == APITypeOpenAI {
						reply = `{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":25}}}`
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
				})})
				var message AssistantMessage
				var err error
				if stream {
					message, err = client.CreateChatCompletionStreamWithOptions(context.Background(), model, messages, nil, options, nil)
				} else {
					message, err = client.CreateChatCompletionWithOptions(context.Background(), model, messages, nil, options)
				}
				if err != nil {
					t.Fatal(err)
				}
				if message.Request.CacheMode != "implicit" {
					t.Fatalf("cache mode = %q", message.Request.CacheMode)
				}
				if !stream && api == APITypeOpenAI && (message.Usage.CachedPromptTokens != 60 || message.Usage.CacheWritePromptTokens != 25 || !message.Usage.CacheUsageAvailable) {
					t.Fatalf("usage = %#v", message.Usage)
				}
			})
		}
	}
}

func TestPromptCacheUnknownFieldRetriesOnceAndCachesEndpointDowngrade(t *testing.T) {
	var cacheFields []bool
	client := NewClient("", "https://cache.test/v1", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		cacheFields = append(cacheFields, strings.Contains(string(body), `"prompt_cache_key"`))
		status, reply := http.StatusOK, providerJSONSuccess(APITypeOpenAI, "done")
		if len(cacheFields) == 1 {
			status, reply = http.StatusBadRequest, `{"error":{"message":"Unknown field prompt_cache_key"}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
	})})
	prompt := NewFrozenPromptSnapshot("stable", "")
	options := ChatCompletionOptions{}
	options.PromptCache = buildPromptCacheOptions(APITypeOpenAI, "gpt-5.6", prompt, nil, options)
	messages := []map[string]any{{"role": "system", "content": "stable"}, {"role": "user", "content": "hello"}}

	first, err := client.CreateChatCompletionWithOptions(context.Background(), "gpt-5.6", messages, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Request.CacheDowngraded || first.Request.CacheDowngradeReason != "provider_rejected_cache_fields" {
		t.Fatalf("first request metadata = %#v", first.Request)
	}
	second, err := client.CreateChatCompletionWithOptions(context.Background(), "gpt-5.6", messages, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Request.CacheDowngraded || second.Request.CacheDowngradeReason != "capability_cache" {
		t.Fatalf("second request metadata = %#v", second.Request)
	}
	if len(cacheFields) != 3 || !cacheFields[0] || cacheFields[1] || cacheFields[2] {
		t.Fatalf("cache fields by attempt = %#v, want [true false false]", cacheFields)
	}
}

func TestPromptCacheDoesNotRetryUnrelatedRequestError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(writer, `{"error":{"message":"invalid user message"}}`)
	}))
	defer server.Close()

	client := NewClient("", server.URL, server.Client())
	options := ChatCompletionOptions{}
	options.PromptCache = buildPromptCacheOptions(APITypeOpenAI, "gpt-5.6", NewFrozenPromptSnapshot("stable", ""), nil, options)
	_, err := client.CreateChatCompletionWithOptions(context.Background(), "gpt-5.6", []map[string]any{{"role": "system", "content": "stable"}}, nil, options)
	if err == nil {
		t.Fatal("unrelated 400 returned nil error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no cache downgrade retry", attempts)
	}
	if client.capabilities.disabled(promptCacheCapabilityKey) {
		t.Fatal("unrelated 400 disabled prompt caching for endpoint")
	}
}

func TestAnthropicKeepsLaterDeveloperContextAfterCachedHistory(t *testing.T) {
	definitions := tools.Definitions()[:2]
	prompt := NewFrozenPromptSnapshot("stable instructions", "session memory")
	history := []map[string]any{
		{"role": "system", "content": "stable instructions"},
		{"role": "system", "content": "session memory"},
		{"role": "user", "content": "fix the bug"},
		{"role": "assistant", "content": nil, "tool_calls": []ToolCall{{ID: "tc-1", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}}},
		{"role": "tool", "tool_call_id": "tc-1", "content": "package a"},
	}
	build := func(messages []map[string]any) anthropicRequest {
		t.Helper()
		options := ChatCompletionOptions{}
		options.PromptCache = buildPromptCacheOptions(APITypeAnthropic, "claude-sonnet", prompt, definitions, options)
		request, err := buildAnthropicRequest("claude-sonnet", messages, definitions, options, false)
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	withTask := func(messages []map[string]any, state string) []map[string]any {
		return append(duplicateMessages(messages), map[string]any{"role": "developer", "content": taskProgressInstructions + "\n" + state})
	}

	first := build(withTask(history, `{"status":"reading"}`))
	if first.CacheControl != nil {
		t.Fatalf("top-level automatic cache would land on volatile task state: %#v", first.CacheControl)
	}
	if len(first.System) != 2 {
		t.Fatalf("system blocks = %#v, want only the leading stable prompt and memory", first.System)
	}
	last := first.Messages[len(first.Messages)-1]
	if last.Role != "user" || len(last.Content) != 2 {
		t.Fatalf("last message = %#v, want tool_result followed by task reminder", last)
	}
	result, _ := last.Content[0].(map[string]any)
	reminder, _ := last.Content[1].(map[string]any)
	if result["type"] != "tool_result" || result["cache_control"] == nil {
		t.Fatalf("tool result = %#v, want explicit breakpoint before task state", result)
	}
	if text, _ := reminder["text"].(string); !strings.Contains(text, `"status":"reading"`) || reminder["cache_control"] != nil {
		t.Fatalf("task reminder = %#v", reminder)
	}

	grown := append(duplicateMessages(history),
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []ToolCall{{ID: "tc-2", Function: FunctionCall{Name: "task_update", Arguments: `{"status":"editing"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "tc-2", "content": "ok"},
	)
	second := build(withTask(grown, `{"status":"editing"}`))
	firstSystem, _ := json.Marshal(first.System)
	secondSystem, _ := json.Marshal(second.System)
	if string(firstSystem) != string(secondSystem) {
		t.Fatalf("task state change altered system prefix:\n%s\n%s", firstSystem, secondSystem)
	}
	stripCache := func(messages []anthropicMessage) string {
		encoded, _ := json.Marshal(messages)
		var decoded []map[string]any
		_ = json.Unmarshal(encoded, &decoded)
		for _, message := range decoded {
			content, _ := message["content"].([]any)
			for _, block := range content {
				delete(block.(map[string]any), "cache_control")
			}
		}
		normalized, _ := json.Marshal(decoded)
		return string(normalized)
	}
	firstHistory := append([]anthropicMessage{}, first.Messages...)
	firstHistory[len(firstHistory)-1].Content = firstHistory[len(firstHistory)-1].Content[:1]
	prefix := strings.TrimSuffix(stripCache(firstHistory), "]")
	if !strings.HasPrefix(stripCache(second.Messages), prefix) {
		t.Fatalf("second request does not extend cached history:\nfirst  %s\nsecond %s", stripCache(firstHistory), stripCache(second.Messages))
	}
}
