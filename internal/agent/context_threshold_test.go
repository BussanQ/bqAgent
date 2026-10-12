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
			config := DefaultContextConfig()
			config.SummarizationEnabled = mode != "prune only"
			client := &stubClient{optionResponses: []AssistantMessage{{Content: "important decisions"}}}
			if mode == "summary failure" {
				client.optionErrors = []error{errors.New("summary unavailable")}
			}
			app := NewWithOptions(client, "test", Options{Context: config, ToolDefinitions: []tools.Definition{}})
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
	for _, count := range []int{200000, 200001} {
		config := DefaultContextConfig()
		config.SummarizationEnabled = false
		client := &exactCountingStubClient{stubClient: &stubClient{}, counts: []InputTokenCount{{Tokens: count, Exact: true}, {Tokens: 8000, Exact: true}}}
		app := NewWithOptions(client, "test", Options{Context: config})
		var usage turn.ContextUsage
		ctx := WithContextUsage(context.Background(), func(value turn.ContextUsage) { usage = value })
		// Between 80% and 85% of the trigger: no exact count yet.
		app.buildRequestMessages(ctx, []map[string]any{{"role": "user", "content": strings.Repeat("a", 650000)}}, nil, ChatCompletionOptions{})
		if len(client.countMessages) != 0 {
			t.Fatal("exact counting used the post-compaction target")
		}
		_, compacted := app.buildRequestMessages(ctx, []map[string]any{{"role": "user", "content": strings.Repeat("b", 690000)}}, nil, ChatCompletionOptions{})
		if (compacted != nil) != (count > 200000) || usage.Estimated {
			t.Fatalf("provider count %d: compacted=%t usage=%+v", count, compacted != nil, usage)
		}
	}
}

func TestBoundWorkingMessagesUsesCompressionTrigger(t *testing.T) {
	config := DefaultContextConfig()
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
	config := DefaultContextConfig()
	config.MaxInputTokens = 100000
	config.TargetInputTokens = 150000
	config = normalizeContextConfig(config)
	if config.CompressionTriggerTokens != 96000 || config.TargetInputTokens > config.CompressionTriggerTokens {
		t.Fatalf("invalid effective budget: %+v", config)
	}
}
