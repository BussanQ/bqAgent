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
	// By default each later delta carries its share of the usage (4 ASCII
	// chars per token), so usage and the streamed estimate agree.
	fill := "x"
	if tokens := client.usage.CompletionTokens - client.usage.ReasoningTokens - 1; tokens > 0 && len(client.offsets) > 1 {
		if repeated := strings.Repeat("x", 4*tokens/(len(client.offsets)-1)); repeated != "" {
			fill = repeated
		}
	}
	for index, offset := range client.offsets {
		client.clock = start.Add(100*time.Millisecond + offset)
		chunk := fill
		if index == 0 {
			chunk = "x"
		}
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
		{name: "paced", offsets: ms(0, 250, 500, 750, 1000, 1250), output: 1001, want: 800},
		{name: "paced_high_rate", offsets: ms(0, 250, 500, 750, 1000, 1250), output: 5001, want: 4000},
		{name: "single_update", offsets: ms(0), output: 1001},
		{name: "two_updates_over_min_window", offsets: ms(0, 1000), output: 1001, want: 1000},
		{name: "same_tick", offsets: ms(0, 0, 0, 0, 0, 0), output: 1001},
		// A few-millisecond window right after the first token is what made
		// the rate spike; it is no longer reported.
		{name: "short_window_rejected", offsets: ms(0, 1, 2, 3, 4, 5), output: 2},
		{name: "sub_second_window_rejected", offsets: ms(0, 50, 100, 150, 200, 250), output: 1001},
		{name: "dense_dispatch_below_old_ceiling", offsets: burst, output: 2},
		{name: "long_ttft_short_window_rejected", offsets: ms(10000, 10001, 10002, 10003, 10004, 10005), output: 2},
		{name: "long_ttft_with_paced_output", offsets: ms(10000, 10250, 10500, 10750, 11000, 11250), output: 1001, want: 800},
		{name: "pause_before_burst_stays_in_denominator", offsets: ms(0, 2000, 2001, 2002, 2003, 2004), output: 1001, want: 1000 / 2.004},
		{name: "pause_dominated_burst_stays_in_denominator", offsets: ms(0, 50, 100, 3000, 3001, 3002, 3003, 3004), output: 1001, want: 1000 / 3.004},
		{name: "real_pause_remains_in_denominator", offsets: ms(0, 100, 400, 900, 1200, 1500), output: 1001, want: 1000 / 1.5},
		// No reasoning deltas reached the window: the reasoning was hidden and
		// generated before it, so its tokens are excluded.
		{name: "hidden_reasoning_excluded", offsets: ms(0, 250, 500, 750, 1000, 1250), output: 1201, reasoning: 200, want: 800},
		// Without usage the text estimate (one "x" = 0.25 token) is the rate.
		{name: "missing_usage", offsets: ms(0, 250, 500, 750, 1000, 1250), want: 1},
		{name: "empty_updates_are_not_samples", offsets: ms(0, 250, 500, 750, 1000, 1250), chunks: []string{"x", "", "", "", "", strings.Repeat("x", 4000)}, output: 1001, want: 800},
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
	if len(events) == 0 {
		t.Fatalf("want live samples, got %+v", events)
	}
	// The first sample is not throttled: it arrives once the window reaches
	// minSpeedWindow (10 deltas of 100ms after the first one).
	if want := time.Unix(1, 0).Add(100*time.Millisecond + offsets[10]); !events[0].at.Equal(want) {
		t.Fatalf("first live sample at %s, want %s", events[0].at, want)
	}
	for index := 1; index < len(events)-1; index++ {
		if gap := events[index].at.Sub(events[index-1].at); gap < LiveSpeedInterval {
			t.Fatalf("live samples %d apart, want >= %s: %+v", gap, LiveSpeedInterval, events)
		}
	}
	// Estimate from text: two tokens of "x" per 100ms delta.
	if live := events[0].speed; math.Abs(live-20) > 1e-6 {
		t.Fatalf("live estimate = %v, want 20", live)
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
	// The first two deltas span 1s and yield a live sample; 2000 more arrive
	// as one buffered dump, dropping the average gap below 1ms, so the final
	// rate is unavailable.
	offsets := []time.Duration{0, time.Second}
	for range 2000 {
		offsets = append(offsets, time.Second)
	}
	stub := &timedGenerationClient{clock: time.Unix(1, 0), offsets: offsets, usage: TokenUsage{CompletionTokens: 2002}}
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

func TestLiveSpeedIgnoresBurstRightAfterFirstToken(t *testing.T) {
	// A large chunk 3ms after the first token used to report thousands of
	// tok/s. Nothing is reported until the window is long enough, and the
	// first value then reflects the steady rate.
	offsets := []time.Duration{0, 3 * time.Millisecond}
	chunks := []string{"x", strings.Repeat("x", 4000)}
	for index := 1; index <= 12; index++ {
		offsets = append(offsets, 3*time.Millisecond+time.Duration(index)*100*time.Millisecond)
		chunks = append(chunks, strings.Repeat("x", 40))
	}
	stub := &timedGenerationClient{clock: time.Unix(1, 0), offsets: offsets, chunks: chunks}
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("want a live sample once the window is long enough")
	}
	// (4000+10*40)/4 tokens over 1.003s ~= 1097; the old 3ms window gave ~333k.
	for _, speed := range events {
		if speed > 2000 {
			t.Fatalf("live speed spiked: %v", events)
		}
	}
}

func TestLiveSpeedReportsFirstSampleOfEachCall(t *testing.T) {
	var events []speedEvent
	var clock time.Time
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speedEvent{clock, speed}) })
	offsets := make([]time.Duration, 12)
	for index := range offsets {
		offsets[index] = time.Duration(index) * 100 * time.Millisecond
	}
	stub := &timedGenerationClient{clock: time.Unix(1, 0), offsets: offsets}
	client := &generationMetricsClient{inner: stub, now: func() time.Time { clock = stub.clock; return stub.clock }}
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// The second call streams twice as fast, starting well within the first
	// call's throttle window.
	stub.chunks = make([]string, len(offsets))
	for index := range stub.chunks {
		stub.chunks[index] = "xx"
	}
	secondStart := stub.clock
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var secondLive *speedEvent
	for index := range events {
		if events[index].at.After(secondStart) && events[index].at.Before(stub.clock) {
			secondLive = &events[index]
			break
		}
	}
	if secondLive == nil || math.Abs(secondLive.speed-5) > 1e-6 {
		t.Fatalf("second call's first live sample was throttled: %+v", events)
	}
}

// reasoningThenTextClient streams reasoning (or tool arguments) through the
// native adapter hook, then a short visible answer.
type reasoningThenTextClient struct {
	clock  time.Time
	steps  int
	answer string
	usage  TokenUsage
	// toolArguments streams the steps as tool arguments instead of reasoning.
	toolArguments bool
	// hiddenReasoning announces an unstreamed reasoning item first.
	hiddenReasoning bool
	// chunk overrides the per-step delta text.
	chunk string
	// summary streams the steps as a reasoning summary.
	summary bool
}

func (client *reasoningThenTextClient) CreateChatCompletion(context.Context, string, []map[string]any, []tools.Definition) (AssistantMessage, error) {
	panic("streaming test only")
}

func (client *reasoningThenTextClient) CreateChatCompletionStream(ctx context.Context, _ string, _ []map[string]any, _ []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	if client.hiddenReasoning {
		reportHiddenReasoning(ctx)
	}
	chunk := client.chunk
	if chunk == "" {
		chunk = strings.Repeat("r", 40)
	}
	for range client.steps {
		client.clock = client.clock.Add(100 * time.Millisecond)
		if client.toolArguments {
			reportOutputDelta(ctx, chunk)
		} else if client.summary {
			reportReasoningSummaryDelta(ctx, chunk)
		} else {
			reportReasoningDelta(ctx, chunk)
		}
	}
	client.clock = client.clock.Add(100 * time.Millisecond)
	if client.answer != "" {
		onChunk(client.answer)
	}
	return AssistantMessage{Content: client.answer, Usage: client.usage}, nil
}

func TestSpeedCountsReasoningAndToolArgumentDeltas(t *testing.T) {
	// 3s of reasoning then one visible chunk: the visible text alone has no
	// window, but the output stream does, so speed appears live and at the end.
	stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 30, answer: "done", usage: TokenUsage{CompletionTokens: 301, ReasoningTokens: 300}}
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	ctx, collector := withTurnGenerationCollector(ctx)
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics := collector.metricsFor(message.FinalContent())
	if want := 300 / 3.0; math.Abs(metrics.TokensPerSecond-want) > 1e-6 {
		t.Fatalf("TPS = %v, want %v", metrics.TokensPerSecond, want)
	}
	// TTFT is still measured to the first visible text, not to reasoning.
	if metrics.FirstTokenLatency != 3100*time.Millisecond {
		t.Fatalf("TTFT = %s, want 3.1s", metrics.FirstTokenLatency)
	}
	// The live estimate during reasoning already equals the final rate here,
	// so identical values collapse into one event.
	if len(events) == 0 || events[0] <= 0 || events[len(events)-1] != metrics.TokensPerSecond {
		t.Fatalf("live events = %v, final = %v", events, metrics.TokensPerSecond)
	}
}

func TestSpeedFromToolOnlyCall(t *testing.T) {
	// A call that only streams tool arguments still reports a live rate.
	stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 20, usage: TokenUsage{CompletionTokens: 201}, toolArguments: true}
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	ctx, collector := withTurnGenerationCollector(ctx)
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[len(events)-1] <= 0 {
		t.Fatalf("tool-only call produced no speed: %v", events)
	}
	if got := collector.metricsFor("final").TokensPerSecond; got != events[len(events)-1] {
		t.Fatalf("turn fallback = %v, want last call rate %v", got, events[len(events)-1])
	}
}

func TestSpeedExcludesHiddenReasoningFromShortToolCall(t *testing.T) {
	// Reproduces the 352 tok/s report: the model reasoned out of band, then
	// streamed ~1.2s of tool arguments. Usage includes the hidden reasoning.
	stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 12, toolArguments: true, usage: TokenUsage{CompletionTokens: 430, ReasoningTokens: 310}}
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// (430-310-1) tokens over the 1.1s window; counting hidden reasoning gave ~390.
	if want := 119 / 1.1; len(events) == 0 || math.Abs(events[len(events)-1]-want) > 1e-6 {
		t.Fatalf("live events = %v, want final %v", events, want)
	}
}

func TestSpeedRatesStreamedOutputWhenHiddenReasoningIsUnreported(t *testing.T) {
	// Reproduces 200+ tok/s mid-turn: the stream carries an encrypted
	// reasoning item, the proxy folds it into completion_tokens without
	// reasoning_tokens, then ~1.1s of tool arguments stream. Only the
	// streamed output is rated.
	stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 12, toolArguments: true, hiddenReasoning: true, usage: TokenUsage{CompletionTokens: 430}}
	var events []float64
	ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
	client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
	if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 11 streamed deltas of 40 chars = 110 tokens over 1.1s; usage gave ~390.
	if want := 110 / 1.1; len(events) == 0 || math.Abs(events[len(events)-1]-want) > 1e-6 {
		t.Fatalf("live events = %v, want final %v", events, want)
	}
}

func TestSpeedKeepsUsageWhenTextEstimateUndercounts(t *testing.T) {
	// Symbols and random strings tokenize far denser than the 4-chars-per-
	// token estimate. Without a hidden reasoning signal, usage stays
	// authoritative even when the estimate is under half of it.
	for _, test := range []struct {
		name  string
		usage TokenUsage
		// hidden marks a reasoning item that is accounted for in usage.
		hidden bool
	}{
		{name: "no_hidden_output", usage: TokenUsage{CompletionTokens: 111}},
		{name: "hidden_reasoning_reported", usage: TokenUsage{CompletionTokens: 411, ReasoningTokens: 300}, hidden: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// 11 deltas of 12 ASCII chars over 1.1s: estimate 33 tokens, usage 110.
			stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 12, toolArguments: true, hiddenReasoning: test.hidden, chunk: "#$%^&*~`|<>!", usage: test.usage}
			var events []float64
			ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
			client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
			if _, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if want := 110 / 1.1; len(events) == 0 || math.Abs(events[len(events)-1]-want) > 1e-6 {
				t.Fatalf("live events = %v, want final %v from usage", events, want)
			}
		})
	}
}

func TestSpeedTreatsReasoningSummaryAsHiddenReasoning(t *testing.T) {
	// Hidden reasoning finishes first, then a summary and the answer stream
	// over 2s. The summary does not mean the reasoning was generated in the
	// window; counting it gave (1101-1)/2 = 550 tok/s.
	for _, test := range []struct {
		name  string
		usage TokenUsage
		want  float64
	}{
		// Reported reasoning is excluded: (1101-1000-1)/2s.
		{name: "reasoning_tokens_reported", usage: TokenUsage{CompletionTokens: 1101, ReasoningTokens: 1000}, want: 50},
		// Unreported: only the streamed estimate, 19 deltas of 40 chars + "done".
		{name: "reasoning_tokens_unreported", usage: TokenUsage{CompletionTokens: 1101}, want: (19*40 + 4) / 4.0 / 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 20, summary: true, answer: "done", usage: test.usage}
			var events []float64
			ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
			ctx, collector := withTurnGenerationCollector(ctx)
			client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
			message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			final := collector.metricsFor(message.FinalContent()).TokensPerSecond
			if math.Abs(final-test.want) > 1e-6 || len(events) == 0 || events[len(events)-1] != final {
				t.Fatalf("final = %v, live = %v, want %v", final, events, test.want)
			}
		})
	}
}

func TestSpeedRatesStreamedOutputWhenHiddenAndStreamedReasoningMix(t *testing.T) {
	// Anthropic can return redacted_thinking alongside thinking_delta. Usage
	// then mixes hidden and in-window reasoning, so it cannot be split.
	for _, test := range []struct {
		name  string
		usage TokenUsage
	}{
		{name: "reasoning_tokens_unreported", usage: TokenUsage{CompletionTokens: 3301}},
		{name: "reasoning_tokens_reported", usage: TokenUsage{CompletionTokens: 3301, ReasoningTokens: 3200}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &reasoningThenTextClient{clock: time.Unix(1, 0), steps: 30, hiddenReasoning: true, answer: "done", usage: test.usage}
			var events []float64
			ctx := WithLiveSpeed(context.Background(), func(speed float64) { events = append(events, speed) })
			ctx, collector := withTurnGenerationCollector(ctx)
			client := &generationMetricsClient{inner: stub, now: func() time.Time { return stub.clock }}
			message, err := client.CreateChatCompletionStream(ctx, testModel, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			// 29 deltas of 40 chars + "done" over 3s ~= 97; usage gave 1100.
			want := (29*40 + 4) / 4.0 / 3
			final := collector.metricsFor(message.FinalContent()).TokensPerSecond
			if math.Abs(final-want) > 1e-6 || len(events) == 0 || events[len(events)-1] != final {
				t.Fatalf("final = %v, live = %v, want %v", final, events, want)
			}
		})
	}
}
