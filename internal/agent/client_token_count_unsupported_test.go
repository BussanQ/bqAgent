package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type tokenCountRoundTripper func(*http.Request) *http.Response

func (f tokenCountRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func tokenCountResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestCountInputTokensCachesUnsupportedEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		apiType APIType
		status  int
	}{
		{"responses404", APITypeOpenAIResponse, http.StatusNotFound},
		{"anthropic404", APITypeAnthropic, http.StatusNotFound},
		{"anthropic405", APITypeAnthropic, http.StatusMethodNotAllowed},
		{"responses501", APITypeOpenAIResponse, http.StatusNotImplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			httpClient := &http.Client{Transport: tokenCountRoundTripper(func(*http.Request) *http.Response {
				calls.Add(1)
				return tokenCountResponse(tc.status, `{"error":"nope"}`)
			})}
			client := NewClientWithAPIType("k", "https://tokencount-"+tc.name+".invalid/v1", tc.apiType, httpClient)
			for i := 0; i < 3; i++ {
				_, err := client.CountInputTokens(context.Background(), "m", []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{})
				if !errors.Is(err, ErrInputTokenCountUnsupported) {
					t.Fatalf("call %d error = %v, want unsupported", i, err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("requests = %d, want 1", calls.Load())
			}
		})
	}
}

func TestCountInputTokensUnsupportedCacheIsolatedByClientAndModel(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: tokenCountRoundTripper(func(*http.Request) *http.Response {
		calls++
		if calls == 1 {
			return tokenCountResponse(http.StatusNotFound, `{"error":"unsupported model"}`)
		}
		return tokenCountResponse(http.StatusOK, `{"input_tokens":42}`)
	})}
	client := NewClientWithAPIType("key", "https://count.invalid/v1", APITypeOpenAIResponse, httpClient)
	_, err := client.CountInputTokens(context.Background(), "unsupported", nil, nil, ChatCompletionOptions{})
	if !errors.Is(err, ErrInputTokenCountUnsupported) {
		t.Fatalf("err=%v", err)
	}
	count, err := client.CountInputTokens(context.Background(), "supported", nil, nil, ChatCompletionOptions{})
	if err != nil || count.Tokens != 42 {
		t.Fatalf("other model: count=%#v err=%v", count, err)
	}
	other := NewClientWithAPIType("other-key", "https://count.invalid/v1", APITypeOpenAIResponse, httpClient)
	count, err = other.CountInputTokens(context.Background(), "unsupported", nil, nil, ChatCompletionOptions{})
	if err != nil || count.Tokens != 42 || calls != 3 {
		t.Fatalf("other client: count=%#v err=%v calls=%d", count, err, calls)
	}
}

func TestCountInputTokensDoesNotCacheAuthOrTransientErrors(t *testing.T) {
	var calls atomic.Int32
	httpClient := &http.Client{Transport: tokenCountRoundTripper(func(*http.Request) *http.Response {
		n := calls.Add(1)
		switch n {
		case 1:
			return tokenCountResponse(http.StatusUnauthorized, `{"error":"bad key"}`)
		case 2:
			return tokenCountResponse(http.StatusServiceUnavailable, `{"error":"busy"}`)
		}
		return tokenCountResponse(http.StatusOK, `{"input_tokens":42}`)
	})}
	client := NewClientWithAPIType("k", "https://tokencount-transient.invalid/v1", APITypeOpenAIResponse, httpClient)
	msgs := []map[string]any{{"role": "user", "content": "hi"}}
	for i := 0; i < 2; i++ {
		_, err := client.CountInputTokens(context.Background(), "m", msgs, nil, ChatCompletionOptions{})
		if err == nil || errors.Is(err, ErrInputTokenCountUnsupported) {
			t.Fatalf("call %d error = %v, want non-unsupported error", i, err)
		}
	}
	count, err := client.CountInputTokens(context.Background(), "m", msgs, nil, ChatCompletionOptions{})
	if err != nil || count.Tokens != 42 || !count.Exact {
		t.Fatalf("count = %#v err = %v, want 42 exact", count, err)
	}
}

func TestCountInputTokensUnsupportedCacheExpires(t *testing.T) {
	var calls atomic.Int32
	httpClient := &http.Client{Transport: tokenCountRoundTripper(func(*http.Request) *http.Response {
		if calls.Add(1) == 1 {
			return tokenCountResponse(http.StatusNotFound, `{"error":"route missing"}`)
		}
		return tokenCountResponse(http.StatusOK, `{"input_tokens":42}`)
	})}
	client := NewClientWithAPIType("k", "https://tokencount-expiry.invalid/v1", APITypeOpenAIResponse, httpClient)
	if _, err := client.CountInputTokens(context.Background(), " m ", nil, nil, ChatCompletionOptions{}); !errors.Is(err, ErrInputTokenCountUnsupported) {
		t.Fatalf("err=%v", err)
	}
	// The cache key ignores surrounding whitespace.
	if _, err := client.CountInputTokens(context.Background(), "m", nil, nil, ChatCompletionOptions{}); !errors.Is(err, ErrInputTokenCountUnsupported) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	client.tokenCountUnsupported.Store("m", time.Now().Add(-time.Second))
	count, err := client.CountInputTokens(context.Background(), "m", nil, nil, ChatCompletionOptions{})
	if err != nil || count.Tokens != 42 || calls.Load() != 2 {
		t.Fatalf("after expiry: count=%#v err=%v calls=%d", count, err, calls.Load())
	}
}

func TestContextLogsTokenCountEndpointDisabledOnce(t *testing.T) {
	httpClient := &http.Client{Transport: tokenCountRoundTripper(func(*http.Request) *http.Response {
		return tokenCountResponse(http.StatusNotFound, `{"error":"route missing"}`)
	})}
	client := NewClientWithAPIType("k", "https://tokencount-log.invalid/v1", APITypeOpenAIResponse, httpClient)
	var log strings.Builder
	app := NewWithOptions(client, "m", Options{LogWriter: &log, Context: ContextConfig{Enabled: true, MaxInputTokens: 2000, CompressionTriggerTokens: 1000, TargetInputTokens: 300, ExactCountTriggerPercent: 1}})
	for i := 0; i < 3; i++ {
		app.measureRequestTokens(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, nil, ChatCompletionOptions{}, true)
	}
	if got := strings.Count(log.String(), "exact token count failed"); got != 1 || !strings.Contains(log.String(), "HTTP 404") {
		t.Fatalf("log = %q", log.String())
	}
}
