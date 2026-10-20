package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bqagent/internal/tools"
	"bqagent/internal/turn"
)

func TestContextWaitsForTriggerThenCompactsBelowTenPercent(t *testing.T) {
	for _, mode := range []string{"summary", "summary failure", "prune only"} {
		t.Run(mode, func(t *testing.T) {
			config := normalizeContextConfig(DefaultContextConfig())
			config.SummarizationEnabled = mode != "prune only"
			client := &stubClient{optionResponses: []AssistantMessage{{Content: "important decisions"}}}
			if mode == "summary failure" {
				client.optionErrors = []error{errors.New("summary unavailable")}
			}
			app := NewWithOptions(client, "test", Options{DisableTurnOutcome: true, Context: config, ToolDefinitions: []tools.Definition{}})
			var usage turn.ContextUsage
			ctx := WithContextUsage(context.Background(), func(value turn.ContextUsage) { usage = value })
			messages := []map[string]any{
				{"role": "system", "content": "instructions"},
				{"role": "user", "content": "early decision " + strings.Repeat("a", 80000)},
				{"role": "assistant", "content": strings.Repeat("b", 400000)},
			}
			request, compacted := app.buildRequestMessages(ctx, messages, nil, ChatCompletionOptions{})
			if compacted != nil || len(request) != len(messages) || len(client.optionMessages) != 0 {
				t.Fatal("context was compacted between the target and trigger")
			}
			before := usage.Tokens
			for i := 0; i < 6; i++ {
				messages = append(messages, map[string]any{"role": "user", "content": "latest task " + strings.Repeat("c", 60000)})
			}
			if estimateMessagesTokens(messages) <= config.CompressionTriggerTokens {
				t.Fatal("fixture does not exceed the trigger")
			}
			request, compacted = app.buildRequestMessages(ctx, messages, nil, ChatCompletionOptions{})
			if compacted == nil || estimateMessagesTokens(request) > config.TargetInputTokens || usage.Tokens*10 >= usage.Limit {
				t.Fatalf("compaction missed target: usage=%+v tokens=%d", usage, estimateMessagesTokens(request))
			}
			if usage.Tokens >= before || usage.Limit != 200000 || !usage.Estimated {
				t.Fatalf("context snapshot did not decrease after compaction: %+v", usage)
			}
			if request[0]["content"] != "instructions" || !strings.Contains(buildSummaryInput(request), "latest task") {
				t.Fatal("compaction lost instructions or the latest question")
			}
			if mode != "prune only" {
				if len(client.optionMessages) != 1 || !strings.Contains(buildSummaryInput(client.optionMessages[0]), "early decision") || estimateMessagesTokens(client.optionMessages[0]) <= config.TargetInputTokens {
					t.Fatal("summary input was prematurely reduced to the post-compaction target")
				}
			}
			if mode == "summary" && !strings.Contains(buildSummaryInput(request), "important decisions") {
				t.Fatal("summary missing from compacted request")
			}
			if _, next := app.buildRequestMessages(ctx, compacted, nil, ChatCompletionOptions{}); next != nil {
				t.Fatal("compacted context immediately triggered another compaction")
			}
		})
	}
}

func TestContextExactCountUsesCompressionTrigger(t *testing.T) {
	for _, count := range []int{169999, 170000, 170001} {
		config := normalizeContextConfig(DefaultContextConfig())
		config.SummarizationEnabled = false
		client := &exactCountingStubClient{stubClient: &stubClient{}, counts: []InputTokenCount{{Tokens: count, Exact: true}, {Tokens: 8000, Exact: true}}}
		app := NewWithOptions(client, "test", Options{DisableTurnOutcome: true, Context: config})
		var usage turn.ContextUsage
		ctx := WithContextUsage(context.Background(), func(value turn.ContextUsage) { usage = value })
		// Below 85% of the total budget: no exact count or compaction yet.
		app.buildRequestMessages(ctx, []map[string]any{{"role": "user", "content": strings.Repeat("a", 650000)}}, nil, ChatCompletionOptions{})
		if len(client.countMessages) != 0 {
			t.Fatal("exact counting used the post-compaction target")
		}
		_, compacted := app.buildRequestMessages(ctx, []map[string]any{{"role": "user", "content": strings.Repeat("b", 690000)}}, nil, ChatCompletionOptions{})
		if (compacted != nil) != (count >= 170000) || usage.Estimated {
			t.Fatalf("provider count %d: compacted=%t usage=%+v", count, compacted != nil, usage)
		}
	}
}

func TestBoundWorkingMessagesUsesCompressionTrigger(t *testing.T) {
	config := normalizeContextConfig(DefaultContextConfig())
	for _, size := range []int{80000, 850000} {
		messages := []map[string]any{{"role": "user", "content": strings.Repeat("x", size)}}
		bounded := BoundWorkingMessages(messages, config)
		if size == 80000 && bounded[0]["content"] != messages[0]["content"] {
			t.Fatal("working snapshot was pruned before reaching trigger")
		}
		if size == 850000 && estimateMessagesTokens(bounded) > config.TargetInputTokens {
			t.Fatal("working snapshot was not pruned to the small target")
		}
	}
}

func TestNormalizeContextTriggerRespectsResponseReserve(t *testing.T) {
	for _, reserve := range []int{4000, 20000} {
		config := DefaultContextConfig()
		config.MaxInputTokens = 100000
		config.ResponseReserveTokens = reserve
		config.TargetInputTokens = 150000
		config = normalizeContextConfig(config)
		if config.CompressionTriggerTokens != min(85000, 100000-reserve) || config.SummaryTriggerTokens != config.CompressionTriggerTokens || config.TargetInputTokens > config.CompressionTriggerTokens {
			t.Fatalf("invalid effective budget: %+v", config)
		}
	}
}

func TestContextCompactsAt85PercentWithoutExactCounter(t *testing.T) {
	for _, tokens := range []int{169999, 170000, 170001} {
		config := DefaultContextConfig()
		config.SummarizationEnabled = false
		app := NewWithOptions(&stubClient{}, "test", Options{DisableTurnOutcome: true, Context: config})
		messages := []map[string]any{{"role": "user", "content": strings.Repeat("x", 700000)}}
		// Calibrate from a previous provider usage value to hit the boundary
		// exactly, without relying on the local text-length estimator.
		app.rememberServerPromptUsage(messages, nil, ChatCompletionOptions{}, tokens)
		_, compacted := app.buildRequestMessages(context.Background(), messages, nil, ChatCompletionOptions{})
		if (compacted != nil) != (tokens >= 170000) {
			t.Fatalf("tokens=%d compacted=%t", tokens, compacted != nil)
		}
	}
}

func TestContextSummaryStartsAtCompressionBoundary(t *testing.T) {
	config := normalizeContextConfig(DefaultContextConfig())
	if shouldSummarize(169999, config) || !shouldSummarize(170000, config) {
		t.Fatalf("summary does not follow compression boundary: %+v", config)
	}
}
