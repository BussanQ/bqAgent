package agent

import (
	"context"
	"sync"
	"time"

	"bqagent/internal/tools"
)

type TurnGenerationMetrics struct {
	Available           bool
	FirstTokenLatency   time.Duration
	PromptTokens        int
	CachedPromptTokens  int
	CacheUsageAvailable bool
	CompletionTokens    int
	ReasoningTokens     int
	GenerationDuration  time.Duration
	TokensPerSecond     float64
	CacheMetrics        TurnCacheMetrics
}

type TurnCacheMetrics struct {
	Available           bool
	Calls               int
	InputTokens         int
	CacheReadTokens     int
	CacheWriteTokens    int
	UncachedInputTokens int
	HitRate             float64
}

type generationMetricsContextKey struct{}

type generationCandidate struct {
	content             string
	firstTokenLatency   time.Duration
	promptTokens        int
	cachedPromptTokens  int
	cacheUsageAvailable bool
	completionTokens    int
	reasoningTokens     int
	generationDuration  time.Duration
	streamUpdates       int
}

// The first text update measures TTFT; any later update gives a measurable
// window, so speed is available from the second delta. Only same-tick
// delivery (a buffered dump with sub-millisecond spacing) is rejected, since
// its window does not reflect generation. Pauses stay in the denominator.
func (candidate generationCandidate) reliableStream() bool {
	const minAverageGap = time.Millisecond
	if candidate.streamUpdates < 1 || candidate.generationDuration <= 0 {
		return false
	}
	return candidate.generationDuration/time.Duration(candidate.streamUpdates) >= minAverageGap
}

type turnGenerationCollector struct {
	mu        sync.Mutex
	candidate generationCandidate
	hasValue  bool
	cache     TurnCacheMetrics
	// lastSpeed is the latest reliable per-call speed, including tool-call
	// rounds, used when the final text call itself has no reliable sample.
	lastSpeed float64
}

// recordSpeed keeps a measurable call rate and returns the rate the turn
// metrics will report if this call ends the turn, so live consumers can
// converge on the final value (0 when no call has been measurable).
func (collector *turnGenerationCollector) recordSpeed(speed float64) float64 {
	if collector == nil {
		return speed
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if speed > 0 {
		collector.lastSpeed = speed
	}
	return collector.lastSpeed
}

func (collector *turnGenerationCollector) recordUsage(usage TokenUsage) {
	if collector == nil {
		return
	}
	collector.mu.Lock()
	collector.cache.Calls++
	collector.cache.InputTokens += usage.PromptTokens
	collector.cache.CacheReadTokens += usage.CachedPromptTokens
	collector.cache.CacheWriteTokens += usage.CacheWritePromptTokens
	collector.cache.Available = collector.cache.Available || usage.CacheUsageAvailable
	collector.mu.Unlock()
}

func (collector *turnGenerationCollector) record(candidate generationCandidate) {
	if collector == nil {
		return
	}
	collector.mu.Lock()
	collector.candidate = candidate
	collector.hasValue = true
	collector.mu.Unlock()
}

func (collector *turnGenerationCollector) metricsFor(result string) TurnGenerationMetrics {
	if collector == nil {
		return TurnGenerationMetrics{}
	}
	collector.mu.Lock()
	candidate, ok := collector.candidate, collector.hasValue
	cache := collector.cache
	lastSpeed := collector.lastSpeed
	collector.mu.Unlock()
	cache.UncachedInputTokens = cache.InputTokens - cache.CacheReadTokens - cache.CacheWriteTokens
	if cache.UncachedInputTokens < 0 {
		cache.UncachedInputTokens = 0
	}
	if cache.InputTokens > 0 {
		cache.HitRate = float64(cache.CacheReadTokens) / float64(cache.InputTokens)
	}
	if !ok || candidate.content != result {
		return TurnGenerationMetrics{CacheMetrics: cache, TokensPerSecond: lastSpeed}
	}

	metrics := TurnGenerationMetrics{
		Available:           true,
		FirstTokenLatency:   candidate.firstTokenLatency,
		PromptTokens:        candidate.promptTokens,
		CachedPromptTokens:  candidate.cachedPromptTokens,
		CacheUsageAvailable: candidate.cacheUsageAvailable,
		CompletionTokens:    candidate.completionTokens,
		ReasoningTokens:     candidate.reasoningTokens,
		GenerationDuration:  candidate.generationDuration,
		CacheMetrics:        cache,
	}
	visibleTokens := candidate.completionTokens - candidate.reasoningTokens
	if visibleTokens > 1 && candidate.generationDuration > 0 && candidate.reliableStream() {
		metrics.TokensPerSecond = float64(visibleTokens-1) / candidate.generationDuration.Seconds()
	} else {
		metrics.TokensPerSecond = lastSpeed
	}
	return metrics
}

func withTurnGenerationCollector(ctx context.Context) (context.Context, *turnGenerationCollector) {
	collector := &turnGenerationCollector{}
	return context.WithValue(ctx, generationMetricsContextKey{}, collector), collector
}

func turnGenerationCollectorFromContext(ctx context.Context) *turnGenerationCollector {
	collector, _ := ctx.Value(generationMetricsContextKey{}).(*turnGenerationCollector)
	return collector
}

type generationMetricsClient struct {
	inner ChatCompletionClient
	now   func() time.Time
}

func instrumentGenerationMetrics(client ChatCompletionClient) ChatCompletionClient {
	if client == nil {
		return nil
	}
	return &generationMetricsClient{inner: client, now: time.Now}
}

func (client *generationMetricsClient) CreateChatCompletion(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition) (AssistantMessage, error) {
	ctx, finishUsage := beginLiveTokenCall(ctx, messages, definitions, ChatCompletionOptions{})
	message, err := client.inner.CreateChatCompletion(ctx, model, messages, definitions)
	finishUsage(message)
	turnGenerationCollectorFromContext(ctx).recordUsage(message.Usage)
	return message, err
}

func (client *generationMetricsClient) CountInputTokens(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (InputTokenCount, error) {
	counter, ok := client.inner.(InputTokenCounter)
	if !ok {
		return InputTokenCount{}, ErrInputTokenCountUnsupported
	}
	return counter.CountInputTokens(ctx, model, messages, definitions, options)
}

func (client *generationMetricsClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {
	ctx, finishUsage := beginLiveTokenCall(ctx, messages, definitions, options)
	var message AssistantMessage
	var err error
	if inner, ok := client.inner.(chatCompletionOptionsClient); ok {
		message, err = inner.CreateChatCompletionWithOptions(ctx, model, messages, definitions, options)
	} else {
		message, err = client.inner.CreateChatCompletion(ctx, model, messages, definitions)
	}
	turnGenerationCollectorFromContext(ctx).recordUsage(message.Usage)
	finishUsage(message)
	return message, err
}

func (client *generationMetricsClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	return client.CreateChatCompletionStreamWithOptions(ctx, model, messages, definitions, ChatCompletionOptions{}, onChunk)
}

func (client *generationMetricsClient) CreateChatCompletionStreamWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions, onChunk func(string)) (AssistantMessage, error) {
	// Only the outer metrics wrapper counts visible deltas. Native adapters
	// separately report reasoning, tool arguments, and authoritative usage.
	countsLiveDeltas := ctx.Value(liveCallKey{}) == nil
	ctx, finishUsage := beginLiveTokenCall(ctx, messages, definitions, options)
	sampler := &streamSampler{startedAt: client.now()}
	live := beginLiveSpeed(ctx)
	if live != nil {
		ctx = context.WithValue(ctx, liveSpeedKey{}, live)
	}
	wrappedOnChunk := func(chunk string) {
		if chunk != "" {
			at := client.now()
			sampler.add(chunk, at)
			live.report(sampler.estimatedSpeed(), at)
		}
		if countsLiveDeltas {
			reportLiveTokenDelta(ctx, chunk)
		}
		if onChunk != nil {
			onChunk(chunk)
		}
	}

	var (
		message AssistantMessage
		err     error
	)
	if inner, ok := client.inner.(chatCompletionStreamOptionsClient); ok {
		message, err = inner.CreateChatCompletionStreamWithOptions(ctx, model, messages, definitions, options, wrappedOnChunk)
	} else {
		message, err = client.inner.CreateChatCompletionStream(ctx, model, messages, definitions, wrappedOnChunk)
	}
	turnGenerationCollectorFromContext(ctx).recordUsage(message.Usage)
	finishUsage(message)
	if err != nil || sampler.firstChunkAt.IsZero() {
		return message, err
	}
	candidate := sampler.candidate(message)
	speed := sampler.estimatedSpeed()
	if len(message.ToolCalls) == 0 {
		// Final usage replaces the text estimate, as in the turn metrics.
		if visible := candidate.completionTokens - candidate.reasoningTokens; visible > 1 && candidate.generationDuration > 0 && candidate.reliableStream() {
			speed = float64(visible-1) / candidate.generationDuration.Seconds()
		}
	}
	// Replace (or retract) live samples with the value the API will report,
	// e.g. when later deltas arrive as a buffered dump and fail the check.
	live.flush(turnGenerationCollectorFromContext(ctx).recordSpeed(speed))
	if len(message.ToolCalls) > 0 || message.FinalContent() == "" {
		return message, nil
	}
	turnGenerationCollectorFromContext(ctx).record(candidate)
	return message, nil
}

// streamSampler records visible text deltas of one streamed call. The first
// delta marks TTFT and is excluded from speed, so both the live estimate and
// the final rate cover only first-to-last-delta generation time.
type streamSampler struct {
	startedAt, firstChunkAt, lastChunkAt time.Time
	streamUpdates                        int
	// units accumulate tokenEstimateUnits after the first delta.
	units int
}

func (sampler *streamSampler) add(chunk string, at time.Time) {
	if sampler.firstChunkAt.IsZero() {
		sampler.firstChunkAt = at
	} else {
		sampler.streamUpdates++
		sampler.units += tokenEstimateUnits(chunk)
	}
	sampler.lastChunkAt = at
}

func (sampler *streamSampler) candidate(message AssistantMessage) generationCandidate {
	return generationCandidate{
		content:             message.FinalContent(),
		firstTokenLatency:   sampler.firstChunkAt.Sub(sampler.startedAt),
		promptTokens:        message.Usage.PromptTokens,
		cachedPromptTokens:  message.Usage.CachedPromptTokens,
		cacheUsageAvailable: message.Usage.CacheUsageAvailable,
		completionTokens:    message.Usage.CompletionTokens,
		reasoningTokens:     message.Usage.ReasoningTokens,
		generationDuration:  sampler.lastChunkAt.Sub(sampler.firstChunkAt),
		streamUpdates:       sampler.streamUpdates,
	}
}

// estimatedSpeed applies the same check as the final rate to the text-based
// token estimate, so it is available from the second delta, before usage.
func (sampler *streamSampler) estimatedSpeed() float64 {
	candidate := sampler.candidate(AssistantMessage{})
	if sampler.units <= 0 || candidate.generationDuration <= 0 || !candidate.reliableStream() {
		return 0
	}
	return float64(sampler.units) / 4 / candidate.generationDuration.Seconds()
}
