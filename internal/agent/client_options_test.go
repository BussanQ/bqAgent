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
