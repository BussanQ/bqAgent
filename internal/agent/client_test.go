package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if len(seenRequest.Tools) != 4 {
		t.Fatalf("tools length = %d, want 4", len(seenRequest.Tools))
	}
	if message.FinalContent() != "done" {
		t.Fatalf("final content = %q, want %q", message.FinalContent(), "done")
	}
}
