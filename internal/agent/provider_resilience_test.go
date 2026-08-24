package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"bqagent/internal/tools"
	apptrace "bqagent/internal/trace"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type errorReader struct {
	err error
}

type metadataClientStub struct{}

func (metadataClientStub) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (AssistantMessage, error) {
	return AssistantMessage{Role: "assistant", Content: "done", Request: ProviderRequestMetadata{
		RetryCount: 1, ReasoningDowngraded: true, ReasoningDowngradeSource: "provider_rejection",
		Recoveries: []ProviderRecovery{{Kind: "provider_retry", Attempt: 2, Category: ProviderErrorServer, StatusCode: 503, Delay: time.Second}},
	}}, nil
}

func (metadataClientStub) CreateChatCompletionStream(context.Context, string, []map[string]any, []tools.Definition, func(string)) (AssistantMessage, error) {
	return AssistantMessage{}, errors.New("not implemented")
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func newTestResilientClient(apiType APIType, baseURL string, httpClient *http.Client, idleTimeout time.Duration) *Client {
	client := NewClientWithOptions("", baseURL, apiType, httpClient, ClientOptions{StreamIdleTimeout: idleTimeout})
	client.retrySleep = func(context.Context, time.Duration) error { return nil }
	client.retryJitter = func(time.Duration) time.Duration { return 0 }
	return client
}

func providerJSONSuccess(apiType APIType, content string) string {
	switch apiType {
	case APITypeOpenAIResponse:
		return fmt.Sprintf(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, content)
	case APITypeAnthropic:
		return fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":"end_turn"}`, content)
	default:
		return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
	}
}

func providerStreamSuccess(apiType APIType, content string) string {
	switch apiType {
	case APITypeOpenAIResponse:
		return fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", content)
	case APITypeAnthropic:
		return fmt.Sprintf("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\ndata: {\"type\":\"message_stop\"}\n\n", content)
	default:
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", content)
	}
}

func TestProviderRetriesTransientHTTPFailures(t *testing.T) {
	for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
			t.Run(string(apiType)+"_"+http.StatusText(statusCode), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					if calls.Add(1) == 1 {
						if statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable {
							writer.Header().Set("Retry-After", "0")
						}
						writer.WriteHeader(statusCode)
						_, _ = fmt.Fprint(writer, `{"error":{"message":"busy"}}`)
						return
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(writer, providerJSONSuccess(apiType, "done"))
				}))
				defer server.Close()

				client := newTestResilientClient(apiType, server.URL, server.Client(), 0)
				message, err := client.CreateChatCompletion(context.Background(), "retry-model", []map[string]any{{"role": "user", "content": "hi"}}, nil)
				if err != nil {
					t.Fatalf("CreateChatCompletion: %v", err)
				}
				if message.FinalContent() != "done" || calls.Load() != 2 || message.Request.RetryCount != 1 {
					t.Fatalf("message=%#v calls=%d metadata=%#v", message, calls.Load(), message.Request)
				}
			})
		}
	}
}

func TestProviderStreamsRetryRateLimitsBeforeOutput(t *testing.T) {
	for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		t.Run(string(apiType), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					writer.Header().Set("Retry-After", "0")
					writer.WriteHeader(http.StatusTooManyRequests)
					return
				}
				_, _ = fmt.Fprint(writer, providerStreamSuccess(apiType, "done"))
			}))
			defer server.Close()
			client := newTestResilientClient(apiType, server.URL, server.Client(), 0)
			message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
			if err != nil || message.FinalContent() != "done" || calls.Load() != 2 || message.Request.RetryCount != 1 {
				t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
			}
		})
	}
}

func TestProviderRetriesOnlyTransientNetworkErrors(t *testing.T) {
	t.Run("connection_refused", func(t *testing.T) {
		var calls atomic.Int32
		httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return nil, syscall.ECONNREFUSED
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(providerJSONSuccess(APITypeOpenAI, "done")))}, nil
		})}
		client := newTestResilientClient(APITypeOpenAI, "https://retry.test", httpClient, 0)
		message, err := client.CreateChatCompletion(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil)
		if err != nil || calls.Load() != 2 || message.Request.RetryCount != 1 {
			t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
		}
	})

	t.Run("permanent_tls", func(t *testing.T) {
		var calls atomic.Int32
		httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, tls.RecordHeaderError{Msg: "invalid TLS record"}
		})}
		client := newTestResilientClient(APITypeOpenAI, "https://tls.test", httpClient, 0)
		_, err := client.CreateChatCompletion(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil)
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorNetwork || providerErr.Transient || calls.Load() != 1 {
			t.Fatalf("err=%#v calls=%d", err, calls.Load())
		}
	})
}

func TestProviderStreamRetryBoundaryUsesSemanticOutput(t *testing.T) {
	tests := []struct {
		name       string
		firstEvent string
		wantCalls  int32
		wantError  bool
	}{
		{name: "text", firstEvent: `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n", wantCalls: 1, wantError: true},
		{name: "reasoning", firstEvent: `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}` + "\n", wantCalls: 1, wantError: true},
		{name: "tool_call", firstEvent: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{"}}]}}]}` + "\n", wantCalls: 1, wantError: true},
		{name: "metadata", firstEvent: `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n", wantCalls: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				call := calls.Add(1)
				body := io.Reader(strings.NewReader(providerStreamSuccess(APITypeOpenAI, "done")))
				if call == 1 {
					body = io.MultiReader(strings.NewReader(test.firstEvent), errorReader{err: io.ErrUnexpectedEOF})
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
			})}
			client := newTestResilientClient(APITypeOpenAI, "https://stream.test", httpClient, 0)
			message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
			if test.wantError && err == nil {
				t.Fatal("CreateChatCompletionStream returned nil error")
			}
			if !test.wantError && (err != nil || message.FinalContent() != "done") {
				t.Fatalf("message=%#v err=%v", message, err)
			}
			if calls.Load() != test.wantCalls {
				t.Fatalf("calls=%d want=%d", calls.Load(), test.wantCalls)
			}
		})
	}
}

func TestProviderRetriesStructuredInBandStreamErrors(t *testing.T) {
	for _, test := range []struct {
		apiType APIType
		event   string
	}{
		{apiType: APITypeOpenAI, event: `data: {"error":{"type":"rate_limit_error","message":"busy"}}` + "\n"},
		{apiType: APITypeOpenAIResponse, event: `data: {"type":"error","error":{"code":"rate_limit_exceeded","message":"busy"}}` + "\n"},
		{apiType: APITypeAnthropic, event: `data: {"type":"error","error":{"type":"overloaded_error","message":"busy"}}` + "\n"},
	} {
		t.Run(string(test.apiType), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				if calls.Add(1) == 1 {
					_, _ = fmt.Fprint(writer, test.event)
					return
				}
				_, _ = fmt.Fprint(writer, providerStreamSuccess(test.apiType, "done"))
			}))
			defer server.Close()
			client := newTestResilientClient(test.apiType, server.URL, server.Client(), 0)
			message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
			if err != nil || message.FinalContent() != "done" || calls.Load() != 2 || message.Request.RetryCount != 1 {
				t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
			}
		})
	}
}

func TestProviderReasoningFallbackAndProcessCache(t *testing.T) {
	for _, apiType := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		t.Run(string(apiType), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				hasReasoning := body["reasoning_effort"] != nil || body["reasoning"] != nil || body["thinking"] != nil || body["output_config"] != nil
				if hasReasoning {
					writer.WriteHeader(http.StatusBadRequest)
					message := "unsupported reasoning_effort parameter"
					if apiType == APITypeOpenAIResponse {
						message = "reasoning.effort is not supported"
					} else if apiType == APITypeAnthropic {
						message = "thinking and output_config are not supported"
					}
					_, _ = fmt.Fprintf(writer, `{"error":{"message":%q}}`, message)
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(writer, providerJSONSuccess(apiType, "done"))
			}))
			defer server.Close()

			client := newTestResilientClient(apiType, server.URL, server.Client(), 0)
			options := ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh}
			first, err := client.CreateChatCompletionWithOptions(context.Background(), "cache-model", []map[string]any{{"role": "user", "content": "hi"}}, nil, options)
			if err != nil || !first.Request.ReasoningDowngraded || first.Request.ReasoningDowngradeSource != "provider_rejection" || first.Request.RetryCount != 0 {
				t.Fatalf("first=%#v err=%v", first, err)
			}
			second, err := client.CreateChatCompletionWithOptions(context.Background(), "cache-model", []map[string]any{{"role": "user", "content": "again"}}, nil, options)
			if err != nil || !second.Request.ReasoningDowngraded || second.Request.ReasoningDowngradeSource != "capability_cache" {
				t.Fatalf("second=%#v err=%v", second, err)
			}
			if calls.Load() != 3 {
				t.Fatalf("calls=%d want=3", calls.Load())
			}
		})
	}
}

func TestProviderReasoningFallbackCachesOnlyAfterSuccess(t *testing.T) {
	var calls atomic.Int32
	var reasoningPresence []bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		hasReasoning := body["reasoning_effort"] != nil
		reasoningPresence = append(reasoningPresence, hasReasoning)
		writer.WriteHeader(http.StatusBadRequest)
		if hasReasoning {
			_, _ = fmt.Fprint(writer, `{"error":{"message":"reasoning_effort is unsupported"}}`)
			return
		}
		_, _ = fmt.Fprint(writer, `{"error":{"message":"invalid messages"}}`)
	}))
	defer server.Close()

	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	options := ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh}
	for range 2 {
		_, err := client.CreateChatCompletionWithOptions(context.Background(), "uncached-model", []map[string]any{{"role": "user", "content": "hi"}}, nil, options)
		if err == nil {
			t.Fatal("CreateChatCompletionWithOptions returned nil error")
		}
	}
	if calls.Load() != 4 || fmt.Sprint(reasoningPresence) != "[true false true false]" {
		t.Fatalf("calls=%d reasoning=%v", calls.Load(), reasoningPresence)
	}
}

func TestProviderReasoningFallbackIgnoresUnrelatedRequestErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(writer, `{"error":{"message":"invalid messages array"}}`)
	}))
	defer server.Close()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	_, err := client.CreateChatCompletionWithOptions(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh})
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Category != ProviderErrorRequest || calls.Load() != 1 || providerErr.Request.ReasoningDowngraded {
		t.Fatalf("err=%#v calls=%d", err, calls.Load())
	}
}

func TestProviderReasoningCacheIsolatedByEndpointAndModel(t *testing.T) {
	newServer := func(calls *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["reasoning_effort"] != nil {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(writer, `{"error":{"message":"reasoning_effort is unsupported"}}`)
				return
			}
			_, _ = fmt.Fprint(writer, providerJSONSuccess(APITypeOpenAI, "done"))
		}))
	}
	var firstCalls, secondCalls atomic.Int32
	firstServer := newServer(&firstCalls)
	defer firstServer.Close()
	secondServer := newServer(&secondCalls)
	defer secondServer.Close()
	options := ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh}
	request := []map[string]any{{"role": "user", "content": "hi"}}
	firstClient := newTestResilientClient(APITypeOpenAI, firstServer.URL, firstServer.Client(), 0)
	for _, model := range []string{"model-a", "model-a", "model-b"} {
		if _, err := firstClient.CreateChatCompletionWithOptions(context.Background(), model, request, nil, options); err != nil {
			t.Fatal(err)
		}
	}
	secondClient := newTestResilientClient(APITypeOpenAI, secondServer.URL, secondServer.Client(), 0)
	if _, err := secondClient.CreateChatCompletionWithOptions(context.Background(), "model-a", request, nil, options); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 5 || secondCalls.Load() != 2 {
		t.Fatalf("first endpoint calls=%d, second endpoint calls=%d", firstCalls.Load(), secondCalls.Load())
	}
}

func TestProviderCombinesTransientRetryAndReasoningFallbackWithinThreeAttempts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		if body["reasoning_effort"] != nil {
			writer.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = fmt.Fprint(writer, `{"error":{"message":"reasoning_effort is not allowed"}}`)
			return
		}
		_, _ = fmt.Fprint(writer, providerJSONSuccess(APITypeOpenAI, "done"))
	}))
	defer server.Close()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	message, err := client.CreateChatCompletionWithOptions(context.Background(), "three-attempt-model", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{ReasoningEffort: ReasoningEffortHigh})
	if err != nil || calls.Load() != 3 || message.Request.RetryCount != 1 || !message.Request.ReasoningDowngraded || len(message.Request.Recoveries) != 2 {
		t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
	}
}

func TestStreamIdleWatchdogCoversHeadersAndRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-request.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, providerStreamSuccess(APITypeOpenAI, "done"))
	}))
	defer server.Close()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 25*time.Millisecond)
	message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
	if err != nil || message.FinalContent() != "done" || calls.Load() != 2 || message.Request.RetryCount != 1 {
		t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
	}
}

func TestStreamIdleWatchdogHeartbeatRenewsBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		for range 3 {
			_, _ = fmt.Fprint(writer, ": ping\n\n")
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = fmt.Fprint(writer, providerStreamSuccess(APITypeOpenAI, "done"))
		flusher.Flush()
	}))
	defer server.Close()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 35*time.Millisecond)
	message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
	if err != nil || message.FinalContent() != "done" || calls.Load() != 1 {
		t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
	}
}

func TestStreamIdleWatchdogRetriesIdleBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.(http.Flusher).Flush()
			select {
			case <-request.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
			return
		}
		_, _ = fmt.Fprint(writer, providerStreamSuccess(APITypeOpenAI, "done"))
	}))
	defer server.Close()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 25*time.Millisecond)
	message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
	if err != nil || message.FinalContent() != "done" || calls.Load() != 2 || message.Request.RetryCount != 1 {
		t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
	}
}

func TestStreamIdleWatchdogCanBeDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = fmt.Fprint(writer, providerStreamSuccess(APITypeOpenAI, "done"))
	}))
	defer server.Close()
	client := newTestResilientClient(APITypeOpenAI, server.URL, server.Client(), 0)
	message, err := client.CreateChatCompletionStream(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil, nil)
	if err != nil || message.FinalContent() != "done" {
		t.Fatalf("message=%#v err=%v", message, err)
	}
}

func TestNonStreamingClientTimeoutRetriesWhenCallerContextActive(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-request.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
			return
		}
		_, _ = fmt.Fprint(writer, providerJSONSuccess(APITypeOpenAI, "done"))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 25 * time.Millisecond
	client := newTestResilientClient(APITypeOpenAI, server.URL, httpClient, 0)
	message, err := client.CreateChatCompletion(context.Background(), "model", []map[string]any{{"role": "user", "content": "hi"}}, nil)
	if err != nil || message.FinalContent() != "done" || calls.Load() != 2 || message.Request.RetryCount != 1 {
		t.Fatalf("message=%#v err=%v calls=%d", message, err, calls.Load())
	}
}

func TestParseRetryAfterSupportsSecondsDatesAndCap(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if delay, ok := parseRetryAfter("3", now); !ok || delay != 3*time.Second {
		t.Fatalf("seconds=(%s,%t)", delay, ok)
	}
	date := now.Add(5 * time.Second).Format(http.TimeFormat)
	if delay, ok := parseRetryAfter(date, now); !ok || delay != 5*time.Second {
		t.Fatalf("date=(%s,%t)", delay, ok)
	}
	if _, ok := parseRetryAfter(now.Add(-time.Second).Format(http.TimeFormat), now); ok {
		t.Fatal("past Retry-After was accepted")
	}
	client := newTestResilientClient(APITypeOpenAI, "https://retry.test", &http.Client{}, 0)
	delay := client.providerRetryDelay(&ProviderError{StatusCode: http.StatusTooManyRequests, HasRetryAfter: true, RetryAfter: time.Minute})
	if delay != providerRetryAfterCap {
		t.Fatalf("capped delay=%s want=%s", delay, providerRetryAfterCap)
	}
	ignored := client.providerRetryDelay(&ProviderError{StatusCode: http.StatusInternalServerError, HasRetryAfter: true, RetryAfter: 5 * time.Second})
	if ignored != providerRetryBaseDelay {
		t.Fatalf("500 Retry-After delay=%s want local backoff %s", ignored, providerRetryBaseDelay)
	}
}

func TestProviderRetryWaitHonorsCancellation(t *testing.T) {
	client := newTestResilientClient(APITypeOpenAI, "https://retry.test", &http.Client{}, 0)
	client.retrySleep = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.waitForProviderRetry(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", err)
	}
}

func TestModelLogIncludesProviderRecoveryMetadata(t *testing.T) {
	var log strings.Builder
	client := instrumentChatCompletionClient(metadataClientStub{}, &log, nil)
	if _, err := client.CreateChatCompletion(context.Background(), "model", nil, nil); err != nil {
		t.Fatal(err)
	}
	text := log.String()
	for _, want := range []string{"[ModelRecovery]", "kind=provider_retry", "retry_count=1", "reasoning_downgraded=true", "reasoning_downgrade_source=provider_rejection"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log missing %q: %s", want, text)
		}
	}
}

func TestAgentRunTraceIncludesProviderRecoveryMetadata(t *testing.T) {
	root := t.TempDir()
	store := apptrace.NewStore(root)
	recorder, err := store.Create("session", "turn", "", "agent", "model", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	app := NewWithOptions(metadataClientStub{}, "model", Options{Trace: recorder})
	if _, err := app.Run(context.Background(), "hi", 1); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Load(recorder.RunID())
	if err != nil {
		t.Fatal(err)
	}
	if meta.RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", meta.RetryCount)
	}
	events, err := os.ReadFile(filepath.Join(root, ".agent", "runs", recorder.RunID(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"type":"provider_retry"`) || !strings.Contains(string(events), `"reasoning_downgraded":true`) {
		t.Fatalf("events missing provider recovery metadata: %s", events)
	}
}
