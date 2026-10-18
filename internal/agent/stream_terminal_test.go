package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Reading past the protocol terminator fails even though EOF never arrives.
type terminalBody struct {
	*strings.Reader
	closed    bool
	extraRead bool
}

func (b *terminalBody) Read(p []byte) (int, error) {
	if b.Len() == 0 {
		b.extraRead = true
		return 0, fmt.Errorf("read after terminal event")
	}
	return b.Reader.Read(p)
}
func (b *terminalBody) Close() error { b.closed = true; return nil }

func TestChatStreamStopsAtTerminalEvent(t *testing.T) {
	for _, tt := range []struct {
		name      string
		api       APIType
		stream    string
		truncated bool
	}{
		{"anthropic", APITypeAnthropic, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\ndata: {\"type\":\"message_stop\"}\n\n", false},
		{"responses", APITypeOpenAIResponse, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"output_tokens\":7}}}\n\n", false},
		{"responses incomplete", APITypeOpenAIResponse, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"output_tokens\":7}}}\n\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &terminalBody{Reader: strings.NewReader(tt.stream)}
			client := NewClientWithAPIType("", "http://test.invalid", tt.api, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})})
			message, err := client.CreateChatCompletionStream(context.Background(), "test", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if body.extraRead || !body.closed {
				t.Fatalf("extra read=%v closed=%v", body.extraRead, body.closed)
			}
			if message.Content != "hello" || message.Usage.CompletionTokens != 7 || message.Completion.OutputTruncated != tt.truncated {
				t.Fatalf("message=%+v", message)
			}
		})
	}
}

var _ io.ReadCloser = (*terminalBody)(nil)
