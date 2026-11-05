package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderErrorBodyIsBoundedForAllProtocols(t *testing.T) {
	huge := strings.Repeat("E", 4*maxProviderErrorBodyBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(huge))
	}))
	defer server.Close()

	for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", apiType, stream), func(t *testing.T) {
				client := NewClientWithAPIType("key", server.URL, apiType, server.Client())
				messages := []map[string]any{{"role": "user", "content": "hi"}}
				var err error
				if stream {
					_, err = client.CreateChatCompletionStream(context.Background(), "m", messages, nil, func(string) {})
				} else {
					_, err = client.CreateChatCompletion(context.Background(), "m", messages, nil)
				}
				var providerErr *ProviderError
				if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusBadRequest {
					t.Fatalf("err = %v, want HTTP 400 provider error", err)
				}
				if len(providerErr.Message) > maxProviderErrorBodyBytes+64 {
					t.Fatalf("error message length = %d, want bounded near %d", len(providerErr.Message), maxProviderErrorBodyBytes)
				}
			})
		}
	}
}

func TestNonStreamingResponseOverLimitFailsWithoutRetry(t *testing.T) {
	previous := maxProviderResponseBytes
	maxProviderResponseBytes = 4 << 10
	t.Cleanup(func() { maxProviderResponseBytes = previous })

	body := `{"choices":[{"message":{"role":"assistant","content":"` + strings.Repeat("a", 64<<10) + `"},"finish_reason":"stop"}]}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		t.Run(string(apiType), func(t *testing.T) {
			requests.Store(0)
			client := NewClientWithAPIType("key", server.URL, apiType, server.Client())
			_, err := client.CreateChatCompletion(context.Background(), "m", []map[string]any{{"role": "user", "content": "hi"}}, nil)
			if !errors.Is(err, errProviderResponseTooLarge) {
				t.Fatalf("err = %v, want response-too-large", err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("requests = %d, oversized response must not be retried", got)
			}
		})
	}
}

func TestStreamingResponseOverLimitFailsWithoutRetry(t *testing.T) {
	previous := maxProviderStreamBytes
	maxProviderStreamBytes = 8 << 10
	t.Cleanup(func() { maxProviderStreamBytes = previous })

	text := strings.Repeat("x", 512)
	cases := map[APIType]struct{ prefix, chunk string }{
		APITypeOpenAI: {chunk: `data: {"choices":[{"index":0,"delta":{"content":"` + text + `"}}]}` + "\n\n"},
		APITypeOpenAIResponse: {chunk: "event: response.output_text.delta\n" +
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"` + text + `"}` + "\n\n"},
		APITypeAnthropic: {
			prefix: "event: message_start\n" + `data: {"type":"message_start","message":{"role":"assistant","usage":{"input_tokens":1}}}` + "\n\n" +
				"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
			chunk: "event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + text + `"}}` + "\n\n",
		},
	}
	for apiType, events := range cases {
		t.Run(string(apiType), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(events.prefix))
				for i := 0; i < 200; i++ {
					if _, err := w.Write([]byte(events.chunk)); err != nil {
						return
					}
				}
			}))
			defer server.Close()

			client := NewClientWithAPIType("key", server.URL, apiType, server.Client())
			var received int
			_, err := client.CreateChatCompletionStream(context.Background(), "m", []map[string]any{{"role": "user", "content": "hi"}}, nil, func(chunk string) {
				received += len(chunk)
			})
			if !errors.Is(err, errProviderResponseTooLarge) {
				t.Fatalf("err = %v, want response-too-large", err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("requests = %d, oversized stream must not be retried", got)
			}
			if int64(received) > maxProviderStreamBytes {
				t.Fatalf("received %d content bytes, want at most stream budget %d", received, maxProviderStreamBytes)
			}
		})
	}
}

func TestLimitedResponseBodyAllowsExactLimit(t *testing.T) {
	body := newLimitedResponseBody(nopReadCloser{strings.NewReader("12345")}, 5)
	data := make([]byte, 16)
	total := 0
	for {
		n, err := body.Read(data[total:])
		total += n
		if err != nil {
			if errors.Is(err, errProviderResponseTooLarge) {
				t.Fatalf("exact-size body reported too large")
			}
			break
		}
	}
	if total != 5 {
		t.Fatalf("read %d bytes, want 5", total)
	}
}

type nopReadCloser struct{ *strings.Reader }

func (nopReadCloser) Close() error { return nil }
