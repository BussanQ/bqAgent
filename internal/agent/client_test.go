package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

func TestClientCreateChatCompletionUsesOpenAICompatibleRequest(t *testing.T) {
	t.Helper()

	var seenPath string
	var seenAuth string
	var seenRequest struct {
		Model    string             `json:"model"`
		Messages []map[string]any   `json:"messages"`
		Tools    []tools.Definition `json:"tools"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenPath = request.URL.Path
		seenAuth = request.Header.Get("Authorization")
		if err := json.NewDecoder(request.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, server.Client())
	message, err := client.CreateChatCompletion(
		context.Background(),
		DefaultModel,
		[]map[string]any{{"role": "user", "content": "hello"}},
		tools.Definitions(),
	)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if seenPath != "/chat/completions" {
		t.Fatalf("request path = %q, want %q", seenPath, "/chat/completions")
	}
	if seenAuth != "Bearer test-key" {
		t.Fatalf("authorization header = %q, want bearer token", seenAuth)
	}
	if seenRequest.Model != DefaultModel {
		t.Fatalf("model = %q, want %q", seenRequest.Model, DefaultModel)
	}
	if len(seenRequest.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(seenRequest.Messages))
	}
	if len(seenRequest.Tools) != 7 {
		t.Fatalf("tools length = %d, want 7", len(seenRequest.Tools))
	}
	if message.FinalContent() != "done" {
		t.Fatalf("final content = %q, want %q", message.FinalContent(), "done")
	}
}

func TestClientCreateChatCompletionParsesInlineToolCallContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>search</think>\n<tool_call>\n{\"name\":\"web_search\",\"parameters\":{\"query\":\"今天天气\"}}\n</tool_call>"}}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, server.Client())
	message, err := client.CreateChatCompletion(
		context.Background(),
		DefaultModel,
		[]map[string]any{{"role": "user", "content": "hello"}},
		tools.Definitions(),
	)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(message.ToolCalls))
	}
	if message.ToolCalls[0].Function.Name != "web_search" {
		t.Fatalf("tool name = %q, want %q", message.ToolCalls[0].Function.Name, "web_search")
	}
	if message.ToolCalls[0].Function.Arguments != `{"query":"今天天气"}` {
		t.Fatalf("tool arguments = %q, want JSON parameters", message.ToolCalls[0].Function.Arguments)
	}
}

func TestAssistantMessageRequestMessageStripsInlineToolCallMarkup(t *testing.T) {
	message := AssistantMessage{
		Role:    "assistant",
		Content: "<think>search</think>\n<tool_call>\n{\"name\":\"web_search\",\"parameters\":{\"query\":\"today\"}}\n</tool_call>",
		ToolCalls: []ToolCall{
			{
				ID:   "inline-tool-1",
				Type: "function",
				Function: FunctionCall{
					Name:      "web_search",
					Arguments: `{"query":"today"}`,
				},
			},
		},
	}

	request := message.RequestMessage()
	if content := request["content"]; content != nil {
		t.Fatalf("content = %#v, want nil for assistant tool call message", content)
	}
	if _, ok := request["tool_calls"]; !ok {
		t.Fatal("request missing tool_calls")
	}
}

func TestClientCreateChatCompletionParsesShorthandInlineToolCallContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<tool_call>web_search search=\"IT科技新闻 今日 最新\"</tool_call>"}}]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL, server.Client())
	message, err := client.CreateChatCompletion(
		context.Background(),
		DefaultModel,
		[]map[string]any{{"role": "user", "content": "hello"}},
		tools.Definitions(),
	)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(message.ToolCalls))
	}
	if message.ToolCalls[0].Function.Name != "web_search" {
		t.Fatalf("tool name = %q, want %q", message.ToolCalls[0].Function.Name, "web_search")
	}
	if message.ToolCalls[0].Function.Arguments != `{"search":"IT科技新闻 今日 最新"}` {
		t.Fatalf("tool arguments = %q, want shorthand args json", message.ToolCalls[0].Function.Arguments)
	}
}

type failingStreamClient struct{}

func (f *failingStreamClient) CreateChatCompletion(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition) (AssistantMessage, error) {
	return AssistantMessage{}, nil
}

func (f *failingStreamClient) CreateChatCompletionStream(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, _ func(string)) (AssistantMessage, error) {
	return AssistantMessage{}, errors.New("stream failed")
}

func TestInstrumentedClientLogsChatCompletionTiming(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{{Content: "done"}},
	}
	var logs bytes.Buffer

	wrapped := instrumentChatCompletionClient(client, &logs)
	_, err := wrapped.CreateChatCompletion(context.Background(), DefaultModel, []map[string]any{{"role": "user", "content": "hello"}}, nil)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned error: %v", err)
	}

	content := logs.String()
	if !strings.Contains(content, "[Model] request=chat") {
		t.Fatalf("logs = %q, want model request log", content)
	}
	if !strings.Contains(content, "model="+DefaultModel) {
		t.Fatalf("logs = %q, want model name", content)
	}
	if !strings.Contains(content, "stream=false") {
		t.Fatalf("logs = %q, want non-stream log", content)
	}
	if !strings.Contains(content, "duration=") {
		t.Fatalf("logs = %q, want duration", content)
	}
	if !strings.Contains(content, "status=success") {
		t.Fatalf("logs = %q, want success status", content)
	}
}

func TestInstrumentedClientLogsStreamErrors(t *testing.T) {
	var logs bytes.Buffer

	wrapped := instrumentChatCompletionClient(&failingStreamClient{}, &logs)
	_, err := wrapped.CreateChatCompletionStream(context.Background(), DefaultModel, []map[string]any{{"role": "user", "content": "hello"}}, nil, nil)
	if err == nil {
		t.Fatal("CreateChatCompletionStream returned nil error, want stream failure")
	}

	content := logs.String()
	if !strings.Contains(content, "[Model] request=chat") {
		t.Fatalf("logs = %q, want model request log", content)
	}
	if !strings.Contains(content, "stream=true") {
		t.Fatalf("logs = %q, want stream log", content)
	}
	if !strings.Contains(content, "status=error") {
		t.Fatalf("logs = %q, want error status", content)
	}
	if !strings.Contains(content, `error="stream failed"`) {
		t.Fatalf("logs = %q, want stream failure detail", content)
	}
}
