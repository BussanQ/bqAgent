package agent

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"bqagent/internal/tools"
)

// Drive the real metrics wrapper with a deterministic clock, without sleeping,
// networking, or depending on the test machine's scheduling/stream buffering.
type timedGenerationClient struct {
	clock   time.Time
	offsets []time.Duration
	chunks  []string
	usage   TokenUsage
}

func (client *timedGenerationClient) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (AssistantMessage, error) {
	panic("streaming test only")
}

func (client *timedGenerationClient) CreateChatCompletionStream(_ context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	start := client.clock
	var content strings.Builder
	for index, offset := range client.offsets {
		client.clock = start.Add(100*time.Millisecond + offset)
		chunk := "x"
		if client.chunks != nil {
			chunk = client.chunks[index]
		}
		content.WriteString(chunk)
		onChunk(chunk)
	}
	return AssistantMessage{Content: content.String(), Usage: client.usage}, nil
}

func TestGenerationMetricsStreamingReliability(t *testing.T) {
	ms := func(values ...int) []time.Duration {
		result := make([]time.Duration, len(values))
		for index, value := range values {
			result[index] = time.Duration(value) * time.Millisecond
		}
		return result
	}
	burst := make([]time.Duration, 501)
	for index := range burst {
		burst[index] = time.Duration(index) * 500 * time.Microsecond
	}
	for _, test := range []struct {
		name      string
		offsets   []time.Duration
		chunks    []string
		output    int
		reasoning int
		want      float64
	}{
		{name: "paced_above_old_ceiling", offsets: ms(0, 50, 100, 150, 200, 250), output: 1001, want: 4000},
		{name: "paced_above_pi_ceiling", offsets: ms(0, 50, 100, 150, 200, 250), output: 5001, want: 20000},
		{name: "single_update", offsets: ms(0), output: 1001},
		{name: "two_updates_are_enough", offsets: ms(0, 1000), output: 1001, want: 1000},
		{name: "same_tick", offsets: ms(0, 0, 0, 0, 0, 0), output: 1001},
		{name: "short_window", offsets: ms(0, 1, 2, 3, 4, 5), output: 2, want: 200},
		{name: "dense_dispatch_below_old_ceiling", offsets: burst, output: 2},
		{name: "long_ttft_excluded", offsets: ms(10000, 10001, 10002, 10003, 10004, 10005), output: 2, want: 200},
		{name: "long_ttft_with_paced_output", offsets: ms(10000, 10050, 10100, 10150, 10200, 10250), output: 1001, want: 4000},
		{name: "pause_before_burst_stays_in_denominator", offsets: ms(0, 2000, 2001, 2002, 2003, 2004), output: 1001, want: 1000 / 2.004},
		{name: "pause_dominated_burst_stays_in_denominator", offsets: ms(0, 50, 100, 3000, 3001, 3002, 3003, 3004), output: 1001, want: 1000 / 3.004},
		{name: "real_pause_remains_in_denominator", offsets: ms(0, 100, 400, 900, 1200, 1500), output: 1001, want: 1000 / 1.5},
		{name: "reasoning_excluded_as_before", offsets: ms(0, 50, 100, 150, 200, 250), output: 1201, reasoning: 200, want: 4000},
		// Without usage the text estimate (one "x" = 0.25 token) is the rate.
		{name: "missing_usage", offsets: ms(0, 50, 100, 150, 200, 250), want: 5},
		{name: "empty_updates_are_not_samples", offsets: ms(0, 50, 100, 150, 200, 250), chunks: []string{"x", "", "", "", "", "x"}, output: 1001, want: 4000},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &timedGenerationClient{
				clock: time.Unix(1, 0), offsets: test.offsets, chunks: test.chunks,
				usage: TokenUsage{CompletionTokens: test.output, ReasoningTokens: test.reasoning},
			}
			client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
			ctx, collector := withTurnGenerationCollector(context.Background())
			message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			metrics := collector.metricsFor(message.FinalContent())
			if !metrics.Available || metrics.FirstTokenLatency <= 0 || metrics.CompletionTokens != test.output {
				t.Fatalf("sampling rejection must preserve usage and TTFT: %+v", metrics)
			}
			if math.Abs(metrics.TokensPerSecond-test.want) > 1e-6 {
				t.Fatalf("TPS=%v, want %v; metrics=%+v", metrics.TokensPerSecond, test.want, metrics)
			}
			if test.want > 0 {
				wantDuration := test.offsets[len(test.offsets)-1] - test.offsets[0]
				if metrics.GenerationDuration != wantDuration {
					t.Fatalf("generation duration=%s, want full text window %s", metrics.GenerationDuration, wantDuration)
				}
			}
		})
	}
}

type speedEvent struct {
	at    time.Time
	speed float64
}

func TestLiveSpeedThrottlesAndMatchesFinalRate(t *testing.T) {
	// 60 deltas every 100ms after a 10s TTFT: the strict rate excludes the
	// first-token wait, so the long TTFT does not drag the value down.
	offsets := make([]time.Duration, 60)
	for index := range offsets {
		offsets[index] = 10*time.Second + time.Duration(index)*100*time.Millisecond
	}
	stub := &timedGenerationClient{clock: time.Unix(1, 0), offsets: offsets, chunks: nil, usage: TokenUsage{CompletionTokens: 119}}
	var events []speedEvent
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speedEvent{stub.clock, speed}) })
	ctx, collector := withTurnGenerationCollector(ctx)
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("want live and final samples, got %+v", events)
	}
	// The first sample is not throttled: it arrives with the second delta.
	if want := time.Unix(1, 0).Add(100*time.Millisecond + offsets[1]); !events[0].at.Equal(want) {
		t.Fatalf("first live sample at %s, want %s", events[0].at, want)
	}
	for index := 1; index < len(events)-1; index++ {
		if gap := events[index].at.Sub(events[index-1].at); gap < LiveSpeedInterval {
			t.Fatalf("live samples %d apart, want >= %s: %+v", gap, LiveSpeedInterval, events)
		}
	}
	// Estimate from text: one "x" per delta = 0.25 token per 100ms.
	if live := events[0].speed; math.Abs(live-2.5) > 1e-6 {
		t.Fatalf("live estimate = %v, want 2.5", live)
	}
	final := collector.metricsFor(message.FinalContent()).TokensPerSecond
	if want := 118 / 5.9; math.Abs(final-want) > 1e-6 || events[len(events)-1].speed != final {
		t.Fatalf("final = %v, last live = %+v, want %v", final, events[len(events)-1], want)
	}
}

func TestGenerationSpeedFallsBackToToolCallRound(t *testing.T) {
	collector := &turnGenerationCollector{}
	collector.recordSpeed(30)
	// A single-delta final text call has no window and keeps the earlier rate.
	collector.record(generationCandidate{content: "answer", completionTokens: 12})
	if got := collector.metricsFor("answer").TokensPerSecond; got != 30 {
		t.Fatalf("fallback speed = %v, want 30", got)
	}
	if got := collector.metricsFor("other").TokensPerSecond; got != 30 {
		t.Fatalf("speed without final candidate = %v, want 30", got)
	}
}

func TestLiveSpeedRetractsSampleRejectedAtStreamEnd(t *testing.T) {
	ms := func(value int) time.Duration { return time.Duration(value) * time.Millisecond }
	// The first two deltas pass the check, the buffered rest drops the whole
	// call's average gap below 1ms, so the final rate is unavailable.
	stub := &timedGenerationClient{clock: time.Unix(1, 0), offsets: []time.Duration{0, ms(2), ms(2), ms(2)}, usage: TokenUsage{CompletionTokens: 4}}
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	ctx, collector := withTurnGenerationCollector(ctx)
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	final := collector.metricsFor(message.FinalContent()).TokensPerSecond
	if len(events) != 2 || events[0] <= 0 || final != 0 || events[1] != final {
		t.Fatalf("live events = %v, final = %v; want a live sample retracted to the final rate", events, final)
	}
}

func TestLiveSpeedFlushesEarlierCallRateWhenFinalCallUnmeasurable(t *testing.T) {
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	ctx, collector := withTurnGenerationCollector(ctx)
	collector.recordSpeed(30)
	stub := &timedGenerationClient{clock: time.Unix(1, 0), offsets: []time.Duration{0, 0, 0}, usage: TokenUsage{CompletionTokens: 4}}
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if final := collector.metricsFor(message.FinalContent()).TokensPerSecond; final != 30 || len(events) != 1 || events[0] != final {
		t.Fatalf("live events = %v, final = %v; want both 30", events, final)
	}
}
