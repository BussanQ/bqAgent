package agent

import (
	"context"
	"sync"
	"time"

	"bqagent/internal/tools"
)

type TurnGenerationMetrics struct {
	Available          bool
	FirstTokenLatency  time.Duration
	CompletionTokens   int
	ReasoningTokens    int
	GenerationDuration time.Duration
	TokensPerSecond    float64
}

type generationMetricsContextKey struct{}

type generationCandidate struct {
	content            string
	firstTokenLatency  time.Duration
	completionTokens   int
	reasoningTokens    int
	generationDuration time.Duration
}

type turnGenerationCollector struct {
	mu        sync.Mutex
	candidate generationCandidate
	hasValue  bool
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
	collector.mu.Unlock()
	if !ok || candidate.content != result {
		return TurnGenerationMetrics{}
	}

	metrics := TurnGenerationMetrics{
		Available:          true,
		FirstTokenLatency:  candidate.firstTokenLatency,
		CompletionTokens:   candidate.completionTokens,
		ReasoningTokens:    candidate.reasoningTokens,
		GenerationDuration: candidate.generationDuration,
	}
	visibleTokens := candidate.completionTokens - candidate.reasoningTokens
	if visibleTokens > 1 && candidate.generationDuration > 0 {
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
	return client.inner.CreateChatCompletion(ctx, model, messages, definitions)
}

func (client *generationMetricsClient) CreateChatCompletionWithOptions(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (AssistantMessage, error) {
	if inner, ok := client.inner.(chatCompletionOptionsClient); ok {
		return inner.CreateChatCompletionWithOptions(ctx, model, messages, definitions, options)
	}
	return client.inner.CreateChatCompletion(ctx, model, messages, definitions)
}

func (client *generationMetricsClient) CreateChatCompletionStream(ctx context.Context, model string, messages []map[string]any, definitions []tools.Definition, onChunk func(string)) (AssistantMessage, error) {
	startedAt := client.now()
	var firstChunkAt, lastChunkAt time.Time
	wrappedOnChunk := func(chunk string) {
		if chunk != "" {
			chunkAt := client.now()
			if firstChunkAt.IsZero() {
				firstChunkAt = chunkAt
			}
			lastChunkAt = chunkAt
		}
		if onChunk != nil {
			onChunk(chunk)
		}
	}

	message, err := client.inner.CreateChatCompletionStream(ctx, model, messages, definitions, wrappedOnChunk)
	if err != nil || firstChunkAt.IsZero() || len(message.ToolCalls) > 0 || message.FinalContent() == "" {
		return message, err
	}
	turnGenerationCollectorFromContext(ctx).record(generationCandidate{
		content:            message.FinalContent(),
		firstTokenLatency:  firstChunkAt.Sub(startedAt),
		completionTokens:   message.Usage.CompletionTokens,
		reasoningTokens:    message.Usage.ReasoningTokens,
		generationDuration: lastChunkAt.Sub(firstChunkAt),
	})
	return message, nil
}
