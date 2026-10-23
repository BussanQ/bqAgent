package main

import (
	"fmt"
	"io"

	"bqagent/internal/agent"
)

// Cache counts are included in input; reasoning is included in output.
func writeTurnTokenUsage(writer io.Writer, usage agent.LiveTokenUsage) {
	if usage.Calls == 0 {
		return
	}
	count := func(tokens int, estimated bool) string {
		if estimated {
			return fmt.Sprintf("~%d", tokens)
		}
		return fmt.Sprint(tokens)
	}
	fmt.Fprintf(writer, "[Tokens] input=%s output=%s total=%s calls=%d",
		count(usage.InputTokens, usage.InputEstimated),
		count(usage.OutputTokens, usage.OutputEstimated),
		count(usage.InputTokens+usage.OutputTokens, usage.InputEstimated || usage.OutputEstimated),
		usage.Calls)
	if usage.CacheUsageAvailable {
		fmt.Fprintf(writer, " cache_read=%d cache_write=%d", usage.CachedInputTokens, usage.CacheWriteTokens)
	}
	if usage.ReasoningTokens > 0 {
		fmt.Fprintf(writer, " reasoning=%d", usage.ReasoningTokens)
	}
	if usage.InputEstimated || usage.OutputEstimated {
		fmt.Fprint(writer, " (~ = estimated)")
	}
	fmt.Fprintln(writer)
}
