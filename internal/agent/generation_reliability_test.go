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
		{name: "insufficient_updates", offsets: ms(0, 1000), output: 1001},
		{name: "same_tick", offsets: ms(0, 0, 0, 0, 0, 0), output: 1001},
		{name: "short_window", offsets: ms(0, 1, 2, 3, 4, 5), output: 2},
		{name: "dense_dispatch_below_old_ceiling", offsets: burst, output: 2},
		{name: "long_ttft_does_not_inflate_evidence", offsets: ms(10000, 10001, 10002, 10003, 10004, 10005), output: 2},
		{name: "long_ttft_with_paced_output", offsets: ms(10000, 10050, 10100, 10150, 10200, 10250), output: 1001, want: 4000},
		{name: "pause_before_burst", offsets: ms(0, 2000, 2001, 2002, 2003, 2004), output: 1001},
		{name: "pause_dominated_burst", offsets: ms(0, 50, 100, 3000, 3001, 3002, 3003, 3004), output: 1001},
		{name: "real_pause_remains_in_denominator", offsets: ms(0, 100, 400, 900, 1200, 1500), output: 1001, want: 1000 / 1.5},
		{name: "reasoning_excluded_as_before", offsets: ms(0, 50, 100, 150, 200, 250), output: 1201, reasoning: 200, want: 4000},
		{name: "missing_usage", offsets: ms(0, 50, 100, 150, 200, 250)},
		{name: "empty_updates_are_not_samples", offsets: ms(0, 50, 100, 150, 200, 250), chunks: []string{"x", "", "", "", "", "x"}, output: 1001},
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
