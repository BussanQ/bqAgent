package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientCreateChatCompletionWithOptionsIncludesResponseFormat(t *testing.T) {
	var seenRequest struct {
		ResponseFormat map[string]any `json:"response_format"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	client := NewClient("", server.URL, server.Client())
	_, err := client.CreateChatCompletionWithOptions(context.Background(), DefaultModel, []map[string]any{{"role": "user", "content": "plan"}}, nil, ChatCompletionOptions{
		ResponseFormat: map[string]any{"type": "json_object"},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletionWithOptions returned error: %v", err)
	}
	if seenRequest.ResponseFormat["type"] != "json_object" {
		t.Fatalf("response_format.type = %#v, want %#v", seenRequest.ResponseFormat["type"], "json_object")
	}
}

func TestReasoningEffortParsing(t *testing.T) {
	for raw, want := range map[string]ReasoningEffort{
		"":       ReasoningEffortAuto,
		"auto":   ReasoningEffortAuto,
		" LOW ":  ReasoningEffortLow,
		"medium": ReasoningEffortMedium,
		"HIGH":   ReasoningEffortHigh,
	} {
		got, err := ParseReasoningEffort(raw)
		if err != nil {
			t.Fatalf("ParseReasoningEffort(%q) returned error: %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseReasoningEffort(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := ParseReasoningEffort("xhigh"); err == nil {
		t.Fatal("ParseReasoningEffort accepted unsupported xhigh")
	}
}

func TestReasoningEffortProviderPayloads(t *testing.T) {
	t.Run("chat_completions", func(t *testing.T) {
		assertReasoningEffortJSON(t, chatCompletionStreamRequest{ReasoningEffort: ReasoningEffortHigh}, []string{"reasoning_effort"}, "high")
		assertReasoningEffortOmitted(t, chatCompletionStreamRequest{}, "reasoning_effort")
	})

	t.Run("responses", func(t *testing.T) {
		request, err := buildOpenAIResponseRequest("model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{ReasoningEffort: ReasoningEffortMedium}, true)
		if err != nil {
			t.Fatalf("buildOpenAIResponseRequest returned error: %v", err)
		}
		assertReasoningEffortJSON(t, request, []string{"reasoning", "effort"}, "medium")
		autoRequest, err := buildOpenAIResponseRequest("model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{}, true)
		if err != nil {
			t.Fatalf("buildOpenAIResponseRequest auto returned error: %v", err)
		}
		assertReasoningEffortOmitted(t, autoRequest, "reasoning")
	})

	t.Run("anthropic", func(t *testing.T) {
		request, err := buildAnthropicRequest("model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{ReasoningEffort: ReasoningEffortLow}, true)
		if err != nil {
			t.Fatalf("buildAnthropicRequest returned error: %v", err)
		}
		assertReasoningEffortJSON(t, request, []string{"thinking", "type"}, "adaptive")
		assertReasoningEffortJSON(t, request, []string{"output_config", "effort"}, "low")
		autoRequest, err := buildAnthropicRequest("model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{}, true)
		if err != nil {
			t.Fatalf("buildAnthropicRequest auto returned error: %v", err)
		}
		assertReasoningEffortOmitted(t, autoRequest, "thinking")
		assertReasoningEffortOmitted(t, autoRequest, "output_config")
	})
}

func TestReasoningEffortStreamOptionsReachChatRequest(t *testing.T) {
	var seen struct {
		ReasoningEffort string `json:"reasoning_effort"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&seen); err != nil {
			t.Fatalf("decode stream request failed: %v", err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient("", server.URL, server.Client())
	message, err := client.CreateChatCompletionStreamWithOptions(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh}, nil)
	if err != nil {
		t.Fatalf("CreateChatCompletionStreamWithOptions returned error: %v", err)
	}
	if message.FinalContent() != "done" {
		t.Fatalf("content = %q, want done", message.FinalContent())
	}
	if seen.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want high", seen.ReasoningEffort)
	}
}

func assertReasoningEffortJSON(t *testing.T, value any, path []string, want string) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal payload failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	var current any = decoded
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("payload path %v is not an object: %s", path, payload)
		}
		current, ok = object[key]
		if !ok {
			t.Fatalf("payload missing path %v: %s", path, payload)
		}
	}
	if current != want {
		t.Fatalf("payload path %v = %#v, want %q", path, current, want)
	}
}

func assertReasoningEffortOmitted(t *testing.T, value any, key string) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal payload failed: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload failed: %v", err)
	}
	if _, ok := decoded[key]; ok {
		t.Fatalf("payload unexpectedly contains %q: %s", key, payload)
	}
}
