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

	t.Run("responses gpt-5.6", func(t *testing.T) {
		options := ChatCompletionOptions{}
		options.PromptCache = buildPromptCacheOptions(APITypeOpenAIResponse, "gpt-5.6-sol", prompt, definitions, options)
		request, err := buildOpenAIResponseRequest("gpt-5.6-sol", messages, definitions, options, false)
		if err != nil {
			t.Fatal(err)
		}
		if request.PromptCacheKey == "" || request.PromptCacheOptions == nil || request.PromptCacheOptions.Mode != "implicit" {
			t.Fatalf("responses cache fields = %#v", request)
		}
		assertOpenAIBreakpoint(t, request.Input[0])
		encodedSecond, _ := json.Marshal(request.Input[1])
		if strings.Contains(string(encodedSecond), "prompt_cache_breakpoint") {
			t.Fatalf("session context unexpectedly has explicit breakpoint: %s", encodedSecond)
		}
	})

	t.Run("responses older model", func(t *testing.T) {
		options := ChatCompletionOptions{}
		options.PromptCache = buildPromptCacheOptions(APITypeOpenAIResponse, "gpt-5.5", prompt, definitions, options)
		request, err := buildOpenAIResponseRequest("gpt-5.5", messages, definitions, options, false)
		if err != nil {
			t.Fatal(err)
		}
		if request.PromptCacheKey == "" || request.PromptCacheOptions != nil {
			t.Fatalf("older-model cache fields = %#v", request)
		}
		encoded, _ := json.Marshal(request.Input[0])
		if strings.Contains(string(encoded), "prompt_cache_breakpoint") {
			t.Fatalf("older model received explicit breakpoint: %s", encoded)
		}
	})

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

func TestOpenAIChatPromptCacheFieldsAndUsage(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"choices":[{"message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":25}}}`)
	}))
	defer server.Close()

	messages := []map[string]any{{"role": "system", "content": "stable"}, {"role": "user", "content": "hello"}}
	prompt := NewFrozenPromptSnapshot("stable", "")
	options := ChatCompletionOptions{}
	options.PromptCache = buildPromptCacheOptions(APITypeOpenAI, "gpt-5.6", prompt, nil, options)
	client := NewClient("", server.URL, server.Client())
	message, err := client.CreateChatCompletionWithOptions(context.Background(), "gpt-5.6", messages, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if body["prompt_cache_key"] == nil || body["prompt_cache_options"] == nil {
		t.Fatalf("chat request = %#v, want cache key and options", body)
	}
	requestMessages, _ := body["messages"].([]any)
	assertOpenAIBreakpoint(t, requestMessages[0])
	if message.Usage.CachedPromptTokens != 60 || message.Usage.CacheWritePromptTokens != 25 || !message.Usage.CacheUsageAvailable {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestPromptCacheUnknownFieldRetriesOnceAndCachesEndpointDowngrade(t *testing.T) {
	var cacheFields []bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		hasCache := strings.Contains(string(body), "prompt_cache_")
		cacheFields = append(cacheFields, hasCache)
		writer.Header().Set("Content-Type", "application/json")
		if len(cacheFields) == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(writer, `{"error":{"message":"Unknown field prompt_cache_options"}}`)
			return
		}
		fmt.Fprint(writer, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer server.Close()

	client := NewClient("", server.URL, server.Client())
	cacheUnsupportedEndpoints.Delete(client.promptCacheCapabilityKey())
	defer cacheUnsupportedEndpoints.Delete(client.promptCacheCapabilityKey())
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
	cacheUnsupportedEndpoints.Delete(client.promptCacheCapabilityKey())
	defer cacheUnsupportedEndpoints.Delete(client.promptCacheCapabilityKey())
	options := ChatCompletionOptions{}
	options.PromptCache = buildPromptCacheOptions(APITypeOpenAI, "gpt-5.6", NewFrozenPromptSnapshot("stable", ""), nil, options)
	_, err := client.CreateChatCompletionWithOptions(context.Background(), "gpt-5.6", []map[string]any{{"role": "system", "content": "stable"}}, nil, options)
	if err == nil {
		t.Fatal("unrelated 400 returned nil error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want no cache downgrade retry", attempts)
	}
	if _, disabled := cacheUnsupportedEndpoints.Load(client.promptCacheCapabilityKey()); disabled {
		t.Fatal("unrelated 400 disabled prompt caching for endpoint")
	}
}

func assertOpenAIBreakpoint(t *testing.T, raw any) {
	t.Helper()
	message, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("message = %#v", raw)
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("message content = %#v", message["content"])
	}
	block, _ := content[len(content)-1].(map[string]any)
	breakpoint, _ := block["prompt_cache_breakpoint"].(map[string]any)
	if breakpoint["mode"] != "explicit" {
		t.Fatalf("content block = %#v, want explicit cache breakpoint", block)
	}
}
