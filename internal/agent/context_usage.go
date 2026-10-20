package agent

import (
	"context"

	"bqagent/internal/turn"
)

type contextUsageKey struct{}

// WithContextUsage reports the request budget before model calls, including
// the reduced snapshot after compaction. It performs no extra token counting.
func WithContextUsage(ctx context.Context, sink func(turn.ContextUsage)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, contextUsageKey{}, sink)
}

func (a *Agent) reportContextUsage(ctx context.Context, measurement requestTokenMeasurement) {
	if sink, ok := ctx.Value(contextUsageKey{}).(func(turn.ContextUsage)); ok {
		sink(turn.ContextUsage{Tokens: measurement.tokens, Limit: a.contextConfig.MaxInputTokens, Estimated: !measurement.exact})
	}
}
