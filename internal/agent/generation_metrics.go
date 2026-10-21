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
	// reasoningStreamed is set when reasoning deltas arrived inside the
	// window, so reasoning tokens were generated within it.
	reasoningStreamed bool
	// hiddenReasoning is set when the stream carried a reasoning item whose
	// content was not streamed (encrypted or redacted).
	hiddenReasoning bool
	// observedTokens estimates the output actually streamed inside the
	// window (deltas after the first), from text length.
	observedTokens float64
}

// minSpeedWindow is the shortest first-to-last-delta window a rate is taken
// from. Like ccstatusline, speed is only meaningful over a long enough span:
// a few-millisecond gap between the first deltas turns one chunk into
// thousands of tok/s. The window still excludes TTFT.
const minSpeedWindow = time.Second

// The first text update measures TTFT. Same-tick delivery (a buffered dump
// with sub-millisecond spacing) is rejected, since its window does not
// reflect generation. Pauses stay in the denominator.
func (candidate generationCandidate) reliableStream() bool {
	const minAverageGap = time.Millisecond
	if candidate.streamUpdates < 1 || candidate.generationDuration < minSpeedWindow {
		return false
	}
	return candidate.generationDuration/time.Duration(candidate.streamUpdates) >= minAverageGap
}

// speed is the output tokens generated inside the window after the first
// output delta. Streamed reasoning counts, as in ccstatusline. Hidden
// reasoning (e.g. Responses models that do not stream it) is generated before
// the window opens, so its tokens are excluded instead of being divided by a
// window that does not contain them. When the stream shows hidden reasoning
// and usage cannot isolate it, only the streamed estimate is rated: either
// reasoning_tokens is missing (some proxies), or reasoning was also streamed
// (e.g. Anthropic thinking mixed with redacted_thinking), so reasoning_tokens
// would mix in-window and hidden tokens. Usage is otherwise authoritative:
// the text estimate is too rough to override it.
func (candidate generationCandidate) speed() float64 {
	if !candidate.reliableStream() {
		return 0
	}
	tokens := float64(candidate.completionTokens)
	if !candidate.reasoningStreamed {
		tokens -= float64(candidate.reasoningTokens)
	}
	tokens--
	if candidate.hiddenReasoning && (candidate.reasoningStreamed || candidate.reasoningTokens == 0) {
		tokens = candidate.observedTokens
	}
	if tokens <= 0 {
		return 0
	}
	return tokens / candidate.generationDuration.Seconds()
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
	if metrics.TokensPerSecond = candidate.speed(); metrics.TokensPerSecond <= 0 {
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
	// The outermost wrapper owns sampling; native adapters feed reasoning and
	// tool-argument deltas into it through reportOutputDelta.
	sampler, ownsSampler := ctx.Value(outputSamplerKey{}).(*streamSampler)
	if !ownsSampler || sampler == nil {
		sampler = &streamSampler{startedAt: client.now(), now: client.now, live: beginLiveSpeed(ctx)}
		ctx = context.WithValue(ctx, outputSamplerKey{}, sampler)
		if sampler.live != nil {
			ctx = context.WithValue(ctx, liveSpeedKey{}, sampler.live)
		}
		ownsSampler = true
	} else {
		ownsSampler = false
	}
	live := sampler.live
	wrappedOnChunk := func(chunk string) {
		if chunk != "" && ownsSampler {
			sampler.text(chunk)
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
	if !ownsSampler || err != nil || sampler.firstOutputAt().IsZero() {
		return message, err
	}
	candidate := sampler.candidate(message)
	speed := candidate.speed()
	if speed <= 0 {
		speed = sampler.estimatedSpeed()
	}
	// Replace (or retract) live samples with the value the API will report,
	// e.g. when later deltas arrive as a buffered dump and fail the check.
	live.flush(turnGenerationCollectorFromContext(ctx).recordSpeed(speed))
	if len(message.ToolCalls) > 0 || message.FinalContent() == "" || candidate.firstTokenLatency < 0 {
		return message, nil
	}
	turnGenerationCollectorFromContext(ctx).record(candidate)
	return message, nil
}

type outputSamplerKey struct{}

// streamSampler records the output deltas of one streamed call: visible text
// plus reasoning and tool arguments, which providers bill as output tokens
// (the same scope as ccstatusline's output speed). The first output delta is
// excluded from speed; the first visible text delta still defines TTFT.
type streamSampler struct {
	mu                                   sync.Mutex
	now                                  func() time.Time
	live                                 *liveSpeedCall
	startedAt, firstChunkAt, lastChunkAt time.Time
	firstTextAt                          time.Time
	streamUpdates                        int
	sawReasoning, hiddenReasoning        bool
	// units accumulate tokenEstimateUnits after the first delta.
	units int
}

type outputKind int

const (
	outputText outputKind = iota
	outputToolArguments
	outputReasoning
	outputReasoningSummary
)

// reportOutputDelta feeds streamed tool arguments into the live token count
// and the call's speed sample.
func reportOutputDelta(ctx context.Context, text string) {
	reportSampledDelta(ctx, text, outputToolArguments)
}

// reportReasoningDelta is reportOutputDelta for streamed reasoning, which
// also marks the call's reasoning tokens as generated inside the window.
func reportReasoningDelta(ctx context.Context, text string) {
	reportSampledDelta(ctx, text, outputReasoning)
}

// reportReasoningSummaryDelta is for streamed reasoning summaries. They are
// streamed output, but the reasoning they summarize was generated hidden
// before them, so its tokens are not attributed to the window.
func reportReasoningSummaryDelta(ctx context.Context, text string) {
	reportSampledDelta(ctx, text, outputReasoningSummary)
}

// reportHiddenReasoning marks that the call generated reasoning that was not
// streamed, e.g. an encrypted Responses reasoning item or a redacted
// Anthropic thinking block.
func reportHiddenReasoning(ctx context.Context) {
	if sampler, _ := ctx.Value(outputSamplerKey{}).(*streamSampler); sampler != nil {
		sampler.mu.Lock()
		sampler.hiddenReasoning = true
		sampler.mu.Unlock()
	}
}

func reportSampledDelta(ctx context.Context, text string, kind outputKind) {
	reportLiveTokenDelta(ctx, text)
	if sampler, _ := ctx.Value(outputSamplerKey{}).(*streamSampler); sampler != nil {
		sampler.record(text, kind)
	}
}

func (sampler *streamSampler) text(chunk string) {
	sampler.record(chunk, outputText)
}

func (sampler *streamSampler) record(chunk string, kind outputKind) {
	if chunk == "" {
		return
	}
	sampler.mu.Lock()
	at := sampler.now()
	switch kind {
	case outputText:
		if sampler.firstTextAt.IsZero() {
			sampler.firstTextAt = at
		}
	case outputReasoning:
		sampler.sawReasoning = true
	case outputReasoningSummary:
		sampler.hiddenReasoning = true
	}
	sampler.add(chunk, at)
	due := sampler.live.due(at)
	var speed float64
	if due {
		speed = sampler.estimatedSpeedLocked()
	}
	sampler.mu.Unlock()
	if due {
		sampler.live.report(speed, at)
	}
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

func (sampler *streamSampler) firstOutputAt() time.Time {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	return sampler.firstChunkAt
}

func (sampler *streamSampler) candidate(message AssistantMessage) generationCandidate {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	return sampler.candidateLocked(message)
}

func (sampler *streamSampler) candidateLocked(message AssistantMessage) generationCandidate {
	firstTokenLatency := time.Duration(-1)
	if !sampler.firstTextAt.IsZero() {
		firstTokenLatency = sampler.firstTextAt.Sub(sampler.startedAt)
	}
	return generationCandidate{
		content:             message.FinalContent(),
		firstTokenLatency:   firstTokenLatency,
		promptTokens:        message.Usage.PromptTokens,
		cachedPromptTokens:  message.Usage.CachedPromptTokens,
		cacheUsageAvailable: message.Usage.CacheUsageAvailable,
		completionTokens:    message.Usage.CompletionTokens,
		reasoningTokens:     message.Usage.ReasoningTokens,
		generationDuration:  sampler.lastChunkAt.Sub(sampler.firstChunkAt),
		streamUpdates:       sampler.streamUpdates,
		reasoningStreamed:   sampler.sawReasoning,
		hiddenReasoning:     sampler.hiddenReasoning,
		observedTokens:      float64(sampler.units) / 4,
	}
}

// estimatedSpeed applies the same check as the final rate to the text-based
// token estimate, so it is available once the window allows, before usage.
func (sampler *streamSampler) estimatedSpeed() float64 {
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	return sampler.estimatedSpeedLocked()
}

func (sampler *streamSampler) estimatedSpeedLocked() float64 {
	candidate := sampler.candidateLocked(AssistantMessage{})
	if sampler.units <= 0 || !candidate.reliableStream() {
		return 0
	}
	return float64(sampler.units) / 4 / candidate.generationDuration.Seconds()
}
