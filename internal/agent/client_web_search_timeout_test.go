package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"bqagent/internal/tools"
)

type nativeSearchStreamStep struct {
	delay time.Duration
	data  string
}

// A timed, cancellation-aware response body for testing the real stream
// watchdog with virtual time, without a network server or wall-clock waits.
type nativeSearchTimedBody struct {
	ctx     context.Context
	steps   []nativeSearchStreamStep
	pending *strings.Reader
	closed  bool
}

func (body *nativeSearchTimedBody) Read(buffer []byte) (int, error) {
	for {
		if body.ctx.Err() != nil {
			return 0, context.Cause(body.ctx)
		}
		if body.pending != nil && body.pending.Len() > 0 {
			return body.pending.Read(buffer)
		}
		if len(body.steps) == 0 {
			return 0, io.EOF
		}
		step := body.steps[0]
		body.steps = body.steps[1:]
		timer := time.NewTimer(step.delay)
		select {
		case <-body.ctx.Done():
			timer.Stop()
			return 0, context.Cause(body.ctx)
		case <-timer.C:
		}
		body.pending = strings.NewReader(step.data)
	}
}

func (body *nativeSearchTimedBody) Close() error {
	body.closed = true
	return nil
}

func TestNativeWebSearchActiveStreamsHaveNoTotalTimeout(t *testing.T) {
	for _, provider := range []string{"Responses", "Magpie Responses", "Magpie Messages"} {
		for _, mode := range []string{"heartbeats renew idle timeout", "idle timeout explicitly disabled"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					idle := 45 * time.Second
					var steps []nativeSearchStreamStep
					if mode == "idle timeout explicitly disabled" {
						idle = 0
						steps = append(steps, nativeSearchStreamStep{3 * time.Minute, ": heartbeat\n\n"})
					} else {
						for range 6 {
							steps = append(steps, nativeSearchStreamStep{30 * time.Second, ": heartbeat\n\n"})
						}
					}
					items, terminal := nativeSearchSSE(t, false)
					options := ClientOptions{NativeWebSearch: "on", StreamIdleTimeout: idle}
					apiType := APITypeOpenAIResponse
					if strings.HasPrefix(provider, "Magpie") {
						options.ProviderID = "magpie"
					}
					if provider == "Magpie Messages" {
						apiType = APITypeAnthropic
						start, call, result := magpieSearchStreamParts(t)
						items, terminal = start+call, result
					}
					steps = append(steps, nativeSearchStreamStep{data: items + terminal})
					var body *nativeSearchTimedBody
					calls := 0
					httpClient := &http.Client{Timeout: 5 * time.Second, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						if _, ok := request.Context().Deadline(); ok {
							t.Fatal("search added a total deadline to an unbounded caller context")
						}
						body = &nativeSearchTimedBody{ctx: request.Context(), steps: steps}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
					})}
					client := NewClientWithOptions("", "https://relay.example/v1", apiType, httpClient, options)
					started := time.Now()
					output, err := client.NativeWebSearch(context.Background(), "model", "query")
					if err != nil || !strings.Contains(output, "Sources:") || calls != 1 || time.Since(started) != 3*time.Minute {
						t.Fatalf("output=%q calls=%d elapsed=%s err=%v", output, calls, time.Since(started), err)
					}
					if !body.closed || httpClient.Timeout != 5*time.Second {
						t.Fatal("search leaked the stream or changed the shared HTTP client timeout")
					}
				})
			})
		}
	}
}

func TestNativeWebSearchIdleAndCallerCancellationRemainEffective(t *testing.T) {
	for _, provider := range []string{"Responses", "Magpie Responses", "Magpie Messages"} {
		for _, stage := range []string{"headers", "body"} {
			for _, stop := range []string{"idle", "user cancellation", "caller deadline"} {
				t.Run(provider+"/"+stage+"/"+stop, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						ctx, cancel := context.WithCancel(context.Background())
						idle, wantElapsed := 30*time.Second, 30*time.Second
						if stop == "caller deadline" {
							cancel()
							ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
							idle, wantElapsed = 5*time.Minute, 3*time.Minute
						}
						defer cancel()
						if stop == "user cancellation" {
							wantElapsed = 10 * time.Second
							go func() { time.Sleep(10 * time.Second); cancel() }()
						}
						deadline, hasDeadline := ctx.Deadline()
						options := ClientOptions{NativeWebSearch: "on", StreamIdleTimeout: idle}
						apiType := APITypeOpenAIResponse
						if strings.HasPrefix(provider, "Magpie") {
							options.ProviderID = "magpie"
						}
						if provider == "Magpie Messages" {
							apiType = APITypeAnthropic
						}
						var requestCtx context.Context
						var body *nativeSearchTimedBody
						calls, fallbackCalls := 0, 0
						client := NewClientWithOptions("", "https://relay.example/v1", apiType, &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
							calls++
							requestCtx = request.Context()
							gotDeadline, gotHasDeadline := requestCtx.Deadline()
							if gotHasDeadline != hasDeadline || hasDeadline && !gotDeadline.Equal(deadline) {
								t.Fatal("search changed the caller's deadline")
							}
							if stage == "headers" {
								<-requestCtx.Done()
								return nil, context.Cause(requestCtx)
							}
							body = &nativeSearchTimedBody{ctx: requestCtx, steps: []nativeSearchStreamStep{
								{data: ": connected\n\n"}, {delay: 10 * time.Minute, data: ": too late\n\n"},
							}}
							return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
						})}, options)
						app := NewWithOptions(client, "model", Options{EnableNativeWebSearch: true, ToolDefinitions: []tools.Definition{tools.WebSearchDefinition()}, Functions: map[string]tools.Function{"web_search": func(fallbackCtx context.Context, _ map[string]any) (string, error) {
							fallbackCalls++
							if fallbackCtx.Err() != nil {
								t.Fatal("API fallback received a cancelled caller context")
							}
							return "API fallback", nil
						}}})
						started := time.Now()
						output, err := app.functions["web_search"](ctx, map[string]any{"query": "query"})
						if calls != 1 || time.Since(started) != wantElapsed || body != nil && !body.closed {
							t.Fatalf("calls=%d elapsed=%s body=%+v", calls, time.Since(started), body)
						}
						if stop == "idle" {
							var idleErr *streamIdleTimeoutError
							if err != nil || output != "API fallback" || fallbackCalls != 1 || !errors.As(context.Cause(requestCtx), &idleErr) || ctx.Err() != nil {
								t.Fatalf("idle timeout did not preserve fallback: output=%q fallback=%d err=%v cause=%v", output, fallbackCalls, err, context.Cause(requestCtx))
							}
						} else {
							wantErr := context.Canceled
							if stop == "caller deadline" {
								wantErr = context.DeadlineExceeded
							}
							if !errors.Is(err, wantErr) || output != "" || fallbackCalls != 0 {
								t.Fatalf("cancelled search continued: output=%q fallback=%d err=%v", output, fallbackCalls, err)
							}
						}
					})
				})
			}
		}
	}
}

func TestNativeWebSearchNonStreamingRetainsHTTPTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := NewClientWithOptions("", "https://relay.example/v1", APITypeAnthropic, &http.Client{Timeout: 2 * time.Minute, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}, ClientOptions{NativeWebSearch: "on", StreamIdleTimeout: time.Second})
		started := time.Now()
		_, err := client.NativeWebSearch(context.Background(), "model", "query")
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || time.Since(started) != 2*time.Minute {
			t.Fatalf("calls=%d elapsed=%s err=%v", calls, time.Since(started), err)
		}
	})
}

func TestNativeWebSearchPauseContinuationLimitWithoutTotalTimeout(t *testing.T) {
	for _, provider := range []string{"Anthropic", "Magpie"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			respond := func(request *http.Request, _ map[string]any) (int, string) {
				calls++
				if _, ok := request.Context().Deadline(); ok {
					t.Fatal("pause_turn continuation added a total timeout")
				}
				return 200, `{"stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"search","name":"web_search","input":{"query":"query"}}]}`
			}
			client := nativeSearchTestClient(t, APITypeAnthropic, respond)
			if provider == "Magpie" {
				client = magpieMessagesTestClient(t, APITypeAnthropic, respond)
			}
			_, err := client.NativeWebSearch(context.Background(), "model", "query")
			if err == nil || !strings.Contains(err.Error(), "continuation limit") || calls != 3 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
