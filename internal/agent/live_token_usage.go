package agent

import (
	"context"
	"sync"

	"bqagent/internal/tools"
)

// LiveTokenUsage covers all model requests in the current user turn, including
// tool-call rounds. Input includes cached input; output includes reasoning when
// the provider includes it in usage. Estimates are never presented as exact.
type LiveTokenUsage struct {
	InputTokens         int
	OutputTokens        int
	InputEstimated      bool
	OutputEstimated     bool
	Calls               int
	CachedInputTokens   int
	CacheWriteTokens    int
	CacheUsageAvailable bool
	ReasoningTokens     int
}

type liveUsageKey struct{}
type liveCallKey struct{}

type liveUsageCollector struct {
	mu    sync.Mutex
	sink  func(LiveTokenUsage)
	calls []*liveUsageCall
	last  LiveTokenUsage
}

type liveUsageCall struct {
	collector         *liveUsageCollector
	usage             LiveTokenUsage
	outputBase        int
	outputUnits       int
	inputReported     bool
	outputReported    bool
	cacheReported     bool
	reasoningReported bool
}

// WithLiveTokenUsage reports changes as soon as provider usage or streamed
// content arrives. It does not make extra token-count requests or delay generation.
func WithLiveTokenUsage(ctx context.Context, sink func(LiveTokenUsage)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, liveUsageKey{}, &liveUsageCollector{sink: sink})
}

func beginLiveTokenCall(ctx context.Context, messages []map[string]any, definitions []tools.Definition, options ChatCompletionOptions) (context.Context, func(AssistantMessage)) {
	collector, _ := ctx.Value(liveUsageKey{}).(*liveUsageCollector)
	if collector == nil || ctx.Value(liveCallKey{}) != nil {
		return ctx, func(AssistantMessage) {}
	}
	call := &liveUsageCall{collector: collector, usage: LiveTokenUsage{
		InputTokens: estimateVisibleRequestTokens(messages, definitions, options), InputEstimated: true, OutputEstimated: true,
	}}
	call.change(func() { collector.calls = append(collector.calls, call) })
	ctx = context.WithValue(ctx, liveCallKey{}, call)
	return ctx, func(message AssistantMessage) {
		call.change(func() {
			// Native stream events already supplied the freshest snapshots.
			// Do not replace them with stale/partial usage at stream completion.
			usage := message.Usage
			if call.inputReported {
				usage.PromptTokens, usage.PromptTokensAvailable = 0, false
			}
			if call.cacheReported {
				usage.CachedPromptTokens, usage.CacheWritePromptTokens, usage.CacheUsageAvailable = 0, 0, false
			}
			if call.outputReported {
				usage.CompletionTokens, usage.CompletionTokensAvailable = 0, false
			}
			if call.reasoningReported {
				usage.ReasoningTokens = 0
			}
			call.applyUsage(usage)
			if call.usage.OutputEstimated {
				units := tokenEstimateUnits(message.FinalContent())
				for _, tool := range message.ToolCalls {
					units += tokenEstimateUnits(tool.Function.Arguments)
				}
				call.usage.OutputTokens = max(call.usage.OutputTokens, (units+3)/4)
			}
		})
	}
}

func reportLiveTokenDelta(ctx context.Context, text string) {
	call, _ := ctx.Value(liveCallKey{}).(*liveUsageCall)
	if call == nil || text == "" {
		return
	}
	call.change(func() {
		call.outputUnits += tokenEstimateUnits(text)
		call.usage.OutputTokens = call.outputBase + (call.outputUnits+3)/4
		call.usage.OutputEstimated = true
	})
}

func reportLiveTokenUsage(ctx context.Context, usage TokenUsage) {
	call, _ := ctx.Value(liveCallKey{}).(*liveUsageCall)
	if call == nil {
		return
	}
	call.change(func() { call.applyUsage(usage) })
}

func (call *liveUsageCall) applyUsage(usage TokenUsage) {
	if usage.CacheUsageAvailable || usage.CachedPromptTokens > 0 || usage.CacheWritePromptTokens > 0 {
		call.cacheReported = true
		call.usage.CacheUsageAvailable = true
		call.usage.CachedInputTokens = max(0, usage.CachedPromptTokens)
		call.usage.CacheWriteTokens = max(0, usage.CacheWritePromptTokens)
	}
	if usage.ReasoningTokens > 0 {
		call.reasoningReported = true
		call.usage.ReasoningTokens = usage.ReasoningTokens
	}
	if usage.PromptTokensAvailable || usage.PromptTokens > 0 {
		call.inputReported = true
		call.usage.InputTokens = max(0, usage.PromptTokens)
		call.usage.InputEstimated = usage.Estimated
	}
	if usage.CompletionTokensAvailable || usage.CompletionTokens > 0 {
		call.outputReported = true
		call.usage.OutputTokens = max(0, usage.CompletionTokens)
		call.usage.OutputEstimated = usage.Estimated
		call.outputBase = call.usage.OutputTokens
		call.outputUnits = 0
	}
}

func (call *liveUsageCall) change(update func()) {
	collector := call.collector
	collector.mu.Lock()
	defer collector.mu.Unlock()
	update()
	snapshot := LiveTokenUsage{Calls: len(collector.calls)}
	for _, item := range collector.calls {
		snapshot.InputTokens += item.usage.InputTokens
		snapshot.OutputTokens += item.usage.OutputTokens
		snapshot.CachedInputTokens += item.usage.CachedInputTokens
		snapshot.CacheWriteTokens += item.usage.CacheWriteTokens
		snapshot.CacheUsageAvailable = snapshot.CacheUsageAvailable || item.usage.CacheUsageAvailable
		snapshot.ReasoningTokens += item.usage.ReasoningTokens
		snapshot.InputEstimated = snapshot.InputEstimated || item.usage.InputEstimated
		snapshot.OutputEstimated = snapshot.OutputEstimated || item.usage.OutputEstimated
	}
	if snapshot != collector.last {
		collector.last = snapshot
		collector.sink(snapshot)
	}
}

// Accumulate fractional estimates across chunks rather than rounding each
// chunk. Non-ASCII text (notably Chinese) must not be counted as ASCII / 4.
func tokenEstimateUnits(text string) int {
	units := 0
	for _, r := range text {
		if r < 128 {
			units++
		} else {
			units += 4
		}
	}
	return units
}

func tokenCountValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
