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
	streamSpan          time.Duration
	streamStalls        time.Duration
}

// Only borrow pi-tps's sampling checks, not its TPS ceiling, stall-adjusted
// speed or full-request fallback. The first text update measures TTFT; the
// subsequent updates provide independent evidence of paced streaming.
func (candidate generationCandidate) reliableStream() bool {
	const minUpdates = 5
	const minAverageGap = time.Millisecond
	const minActiveSpan = 200 * time.Millisecond
	if candidate.streamUpdates < minUpdates || candidate.streamSpan <= 0 {
		return false
	}
	averageGap := candidate.streamSpan / time.Duration(candidate.streamUpdates-1)
	activeSpan := candidate.streamSpan - candidate.streamStalls
	return averageGap >= minAverageGap && activeSpan >= minActiveSpan && candidate.streamStalls < activeSpan
}

type turnGenerationCollector struct {
	mu        sync.Mutex
	candidate generationCandidate
	hasValue  bool
	cache     TurnCacheMetrics
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
	collector.mu.Unlock()
	cache.UncachedInputTokens = cache.InputTokens - cache.CacheReadTokens - cache.CacheWriteTokens
	if cache.UncachedInputTokens < 0 {
		cache.UncachedInputTokens = 0
	}
	if cache.InputTokens > 0 {
		cache.HitRate = float64(cache.CacheReadTokens) / float64(cache.InputTokens)
	}
	if !ok || candidate.content != result {
		return TurnGenerationMetrics{CacheMetrics: cache}
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
	startedAt := client.now()
	var firstChunkAt, lastChunkAt, firstStreamUpdateAt time.Time
	var streamUpdates int
	var streamStalls time.Duration
	wrappedOnChunk := func(chunk string) {
		if chunk != "" {
			chunkAt := client.now()
			if firstChunkAt.IsZero() {
				firstChunkAt = chunkAt
			} else {
				streamUpdates++
				if firstStreamUpdateAt.IsZero() {
					firstStreamUpdateAt = chunkAt
				} else if gap := chunkAt.Sub(lastChunkAt); gap >= 500*time.Millisecond {
					// Use stalls only to detect pause-then-burst delivery. They
					// remain part of generationDuration and therefore of TPS.
					streamStalls += gap
				}
			}
			lastChunkAt = chunkAt
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
	if err != nil || firstChunkAt.IsZero() || len(message.ToolCalls) > 0 || message.FinalContent() == "" {
		return message, err
	}
	var streamSpan time.Duration
	if !firstStreamUpdateAt.IsZero() {
		streamSpan = lastChunkAt.Sub(firstStreamUpdateAt)
	}
	turnGenerationCollectorFromContext(ctx).record(generationCandidate{
		content:             message.FinalContent(),
		firstTokenLatency:   firstChunkAt.Sub(startedAt),
		promptTokens:        message.Usage.PromptTokens,
		cachedPromptTokens:  message.Usage.CachedPromptTokens,
		cacheUsageAvailable: message.Usage.CacheUsageAvailable,
		completionTokens:    message.Usage.CompletionTokens,
		reasoningTokens:     message.Usage.ReasoningTokens,
		generationDuration:  lastChunkAt.Sub(firstChunkAt),
		streamUpdates:       streamUpdates,
		streamSpan:          streamSpan,
		streamStalls:        streamStalls,
	})
	return message, nil
}
