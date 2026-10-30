package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestProviderFourRetriesWithExponentialBackoff(t *testing.T) {
	for _, api := range []APIType{APITypeOpenAI, APITypeOpenAIResponse, APITypeAnthropic} {
		for _, stream := range []bool{false, true} {
			for _, exhausted := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/exhausted=%t", api, stream, exhausted), func(t *testing.T) {
					calls := 0
					httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						calls++
						status, body := http.StatusBadGateway, `{"error":{"message":"upstream unavailable"}}`
						if calls%5 == 0 && !exhausted {
							status, body = http.StatusOK, providerJSONSuccess(api, "done")
							if stream {
								body = providerStreamSuccess(api, "done")
							}
						}
						return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
					})}
					client := newTestResilientClient(api, "https://retry.test", httpClient, 0)
					var delays []time.Duration
					client.retrySleep = func(_ context.Context, delay time.Duration) error {
						delays = append(delays, delay)
						return nil
					}
					// A new model request must get a fresh retry budget and backoff.
					for request := 1; request <= 2; request++ {
						delays = nil
						var message AssistantMessage
						var err error
						if stream {
							message, err = client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
						} else {
							message, err = client.CreateChatCompletion(context.Background(), "test", nil, nil)
						}
						metadata := message.Request
						if exhausted {
							if err == nil {
								t.Fatal("expected retry exhaustion")
							}
							metadata = ProviderRequestMetadataFromError(err)
						} else if err != nil || message.FinalContent() != "done" {
							t.Fatalf("message=%+v err=%v", message, err)
						}
						wantDelays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
						if calls != request*5 || metadata.RetryCount != 4 || !slices.Equal(delays, wantDelays) || len(metadata.Recoveries) != 4 {
							t.Fatalf("calls=%d delays=%v metadata=%+v", calls, delays, metadata)
						}
						for i, recovery := range metadata.Recoveries {
							if recovery.Kind != "provider_retry" || recovery.Attempt != i+2 || recovery.Delay != wantDelays[i] {
								t.Fatalf("recovery[%d]=%+v", i, recovery)
							}
						}
					}
				})
			}
		}
	}
}

func TestProviderRetryDelayPreservesJitterAndRetryAfter(t *testing.T) {
	client := newTestResilientClient(APITypeOpenAI, "https://retry.test", nil, 0)
	client.retryJitter = func(limit time.Duration) time.Duration { return limit }
	for i, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if delay := client.providerRetryDelay(nil, i+1); delay != base+250*time.Millisecond {
			t.Fatalf("retry=%d delay=%s", i+1, delay)
		}
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, after := range []time.Duration{0, 3 * time.Second, time.Minute} {
			for retry := 1; retry <= 4; retry++ {
				err := &ProviderError{StatusCode: status, HasRetryAfter: true, RetryAfter: after}
				if delay := client.providerRetryDelay(err, retry); delay != min(after, 10*time.Second) {
					t.Fatalf("status=%d retry=%d Retry-After=%s delay=%s", status, retry, after, delay)
				}
			}
		}
	}
}

func TestStreamProgressTimeoutThenServerErrorRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			switch calls {
			case 1:
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &nativeSearchTimedBody{ctx: req.Context(), steps: []nativeSearchStreamStep{{2 * time.Minute, ""}}}}, nil
			case 2:
				return &http.Response{StatusCode: 502, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("upstream unavailable"))}, nil
			default:
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(providerStreamSuccess(APITypeOpenAIResponse, "done")))}, nil
			}
		})}
		client := NewClientWithOptions("", "https://test.invalid", APITypeOpenAIResponse, httpClient, ClientOptions{StreamProgressTimeout: time.Minute})
		client.retryJitter = func(time.Duration) time.Duration { return 0 }
		start := time.Now()
		message, err := client.CreateChatCompletionStream(context.Background(), "test", nil, nil, nil)
		if err != nil || message.FinalContent() != "done" || calls != 3 || message.Request.RetryCount != 2 || time.Since(start) != time.Minute+3*time.Second {
			t.Fatalf("message=%+v err=%v calls=%d elapsed=%s", message, err, calls, time.Since(start))
		}
	})
}
