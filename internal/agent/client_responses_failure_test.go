package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesFailureDetails(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		stream           bool
		category         ProviderErrorCategory
	}{
		{"nested stream", `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"connection lost mid-reply (unexpected EOF)"}}}` + "\n\n", "connection lost mid-reply (unexpected EOF)", true, ProviderErrorServer},
		{"top level stream", `data: {"type":"response.failed","error":{"code":"rate_limit_exceeded","message":"quota exceeded"}}` + "\n\n", "quota exceeded", true, ProviderErrorRateLimit},
		{"message fallback", `data: {"type":"error","message":"invalid request"}` + "\n\n", "invalid request", true, ProviderErrorProtocol},
		{"nested code only", `data: {"type":"response.failed","response":{"error":{"code":"invalid_request"}}}` + "\n\n", "invalid_request", true, ProviderErrorProtocol},
		{"missing details", `data: {"type":"response.failed","response":{"status":"failed","error":null}}` + "\n\n", "upstream reported response.failed", true, ProviderErrorProtocol},
		{"nested json", `{"status":"failed","error":{"code":"server_error","message":"upstream disconnected"}}`, "upstream disconnected", false, ProviderErrorServer},
		{"failed json without details", `{"status":"failed"}`, "upstream reported response.failed", false, ProviderErrorProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := newTestResilientClient(APITypeOpenAIResponse, "https://failure.test", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}, 0)
			var err error
			if tc.stream {
				_, err = client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
			} else {
				_, err = client.CreateChatCompletion(context.Background(), "test", nil, nil)
			}
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || !strings.Contains(err.Error(), tc.want) || providerErr.Category != tc.category {
				t.Fatalf("error=%+v, want %s containing %q", err, tc.category, tc.want)
			}
			wantCalls := 1
			if tc.category == ProviderErrorServer || tc.category == ProviderErrorRateLimit {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestResponsesFailureAfterTextDoesNotRetry(t *testing.T) {
	calls := 0
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"server_error\",\"message\":\"connection lost\"}}}\n\n"
	client := newTestResilientClient(APITypeOpenAIResponse, "https://failure.test", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, 0)
	var text strings.Builder
	_, err := client.CreateChatCompletionStream(context.Background(), "test", nil, nil, func(chunk string) { text.WriteString(chunk) })
	if err == nil || !strings.Contains(err.Error(), "connection lost") || calls != 1 || text.String() != "partial answer" {
		t.Fatalf("error=%v calls=%d text=%q", err, calls, text.String())
	}
}
