package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

type taskCheckpointRecorder struct {
	capturingRecorder
	tail []map[string]any
}

func (r *taskCheckpointRecorder) SaveCheckpointSummary(summary string, tail []map[string]any, _ string) error {
	r.checkpoint = summary
	r.tail = duplicateMessages(tail)
	return nil
}

func mergeTaskHistory() []map[string]any {
	return []map[string]any{
		{"role": "system", "content": "system instructions"},
		{"role": "system", "content": "frozen memory"},
		{"role": "user", "content": "/model kiro/model"},
		{"role": "assistant", "content": "model changed"},
		{"role": "user", "content": "将 dev 合入 master"},
		{"role": "assistant", "tool_calls": []ToolCall{{ID: "merge", Function: FunctionCall{Name: "bash", Arguments: `{"command":"git merge --no-ff --no-commit origin/dev"}`}}}},
		{"role": "tool", "tool_call_id": "merge", "content": "CONFLICT reader.rs and pxe-tftp/src/lib.rs; resolving conflicts"},
	}
}

func TestHardPruneNeverDropsLatestTaskAtTinyBudgets(t *testing.T) {
	for _, budget := range []int{1, 5, 20, 40} {
		history := mergeTaskHistory()
		pruned := hardPruneMessagesToBudget(history, budget)
		latest := latestUserMessageIndex(pruned)
		if latest < 0 || pruned[latest]["content"] != "将 dev 合入 master" {
			t.Fatalf("budget=%d lost task: %#v", budget, pruned)
		}
		if !reflect.DeepEqual(pruned[:2], history[:2]) {
			t.Fatalf("budget=%d changed prompt", budget)
		}
	}
}

func TestContextUsageCalibrationCannotEraseCurrentTask(t *testing.T) {
	for _, summarize := range []bool{false, true} {
		client := &stubClient{optionResponses: []AssistantMessage{{Content: "Merge dev into master; resolve reader.rs and pxe-tftp conflicts."}}}
		recorder := &taskCheckpointRecorder{}
		app := NewWithOptions(client, "test", Options{Recorder: recorder, Context: ContextConfig{
			Enabled: true, MaxInputTokens: 2000, CompressionTriggerTokens: 1500,
			TargetInputTokens: 300, KeepLastTurns: 1, SummarizationEnabled: summarize,
		}})
		history := mergeTaskHistory()
		app.rememberServerPromptUsage(history, nil, ChatCompletionOptions{}, 1700)
		request, compacted, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
		if err != nil || compacted == nil || latestUserMessageIndex(request) < 0 {
			t.Fatalf("summarize=%t request=%#v err=%v", summarize, request, err)
		}
		text := buildSummaryInput(request)
		for _, want := range []string{"将 dev 合入 master", "git merge --no-ff --no-commit origin/dev", "CONFLICT reader.rs"} {
			if !strings.Contains(text, want) {
				t.Fatalf("summarize=%t lost %q: %s", summarize, want, text)
			}
		}
		if summarize {
			// The retained tail fits and is sent verbatim (checked above), so the
			// summary covers only the earlier prefix instead of repeating it.
			if len(client.optionMessages) != 1 {
				t.Fatalf("summary requests = %d", len(client.optionMessages))
			}
			summaryInput := buildSummaryInput(client.optionMessages[0])
			if !strings.Contains(summaryInput, "/model kiro/model") || strings.Contains(summaryInput, "CONFLICT reader.rs") || strings.Contains(summaryInput, "将 dev 合入 master") {
				t.Fatalf("summary input = %s", summaryInput)
			}
			if recorder.checkpoint == "" || latestUserMessageIndex(recorder.tail) < 0 {
				t.Fatalf("checkpoint lost task: summary=%q tail=%#v", recorder.checkpoint, recorder.tail)
			}
			start := leadingPromptMessageCount(request) + 1
			if !reflect.DeepEqual(recorder.tail, request[start:]) {
				t.Fatalf("checkpoint tail differs from sent tail")
			}
		}
	}
}

func TestContextRejectsImpossibleBudgetWithoutSendingPromptOnly(t *testing.T) {
	client := &stubClient{}
	app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: true, MaxInputTokens: 100, TargetInputTokens: 50, KeepLastTurns: 1}})
	history := []map[string]any{
		{"role": "system", "content": strings.Repeat("instructions ", 100)},
		{"role": "user", "content": "将 dev 合入 master"},
	}
	request, _, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
	if err == nil || !strings.Contains(err.Error(), "context budget too small") || request != nil {
		t.Fatalf("request=%#v err=%v", request, err)
	}
	if len(client.messages) != 0 || len(client.optionMessages) != 0 {
		t.Fatal("sent request with impossible budget")
	}
}

func TestContextFinalBudgetUsesFreshProviderMeasurement(t *testing.T) {
	client := &funcCountingClient{stubClient: &stubClient{}, count: func(messages []map[string]any) InputTokenCount {
		return InputTokenCount{Tokens: estimateMessagesTokens(messages) * 3, Exact: true}
	}}
	app := NewWithOptions(client, "test", Options{Context: ContextConfig{
		Enabled: true, MaxInputTokens: 2000, CompressionTriggerTokens: 1000,
		TargetInputTokens: 300, KeepLastTurns: 1, ExactCountTriggerPercent: 1,
	}})
	history := mergeTaskHistory()
	history[len(history)-1]["content"] = strings.Repeat("conflict details ", 200)
	request, _, err := app.buildRequestMessagesChecked(context.Background(), history, nil, ChatCompletionOptions{})
	if err != nil || latestUserMessageIndex(request) < 0 {
		t.Fatalf("request=%#v err=%v", request, err)
	}
	if estimateMessagesTokens(request)*3 > 300 {
		t.Fatalf("final provider count exceeds target: %d", estimateMessagesTokens(request)*3)
	}
	if len(client.seen) < 3 || !reflect.DeepEqual(client.seen[len(client.seen)-1], request) {
		t.Fatal("adjusted request was not recounted")
	}
}

func TestSummaryCoversSingleActiveTurn(t *testing.T) {
	client := &stubClient{optionResponses: []AssistantMessage{{Content: "Merge in progress; reader.rs still conflicted."}}}
	app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: true, MaxInputTokens: 2000, TargetInputTokens: 300, KeepLastTurns: 4}})
	history := mergeTaskHistory()
	history = append(history[:2], history[4:]...)
	result, ok := app.summarizeMessages(context.Background(), history, 300)
	if !ok || latestUserMessageIndex(result) < 0 || len(client.optionMessages) != 1 {
		t.Fatalf("result=%#v ok=%t", result, ok)
	}
	for _, want := range []string{"将 dev 合入 master", "CONFLICT reader.rs"} {
		if !strings.Contains(buildSummaryInput(client.optionMessages[0]), want) {
			t.Fatalf("summary lost %q", want)
		}
	}
}

func TestMessageBudgetSubtractsOnlyLocallyEstimatedOverhead(t *testing.T) {
	protected := []map[string]any{{"role": "developer", "content": "current task snapshot"}}
	definitions := []tools.Definition{{Type: "function"}}
	options := ChatCompletionOptions{ResponseFormat: map[string]any{"type": "json_object"}}
	got := messageBudgetForRequest(protected, definitions, options, 300)
	want := 300 - estimateMessagesTokens(protected) - estimateVisibleRequestTokens(nil, definitions, options)
	if got != want || got <= 1 {
		t.Fatalf("budget=%d want=%d", got, want)
	}
}

func TestSummaryIncludesTailOnlyWhenPruningCutsIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  int
		hasTail bool
	}{
		{"tail fits", 2000, false},
		{"tail cut", 60, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubClient{optionResponses: []AssistantMessage{{Content: "summary"}}}
			app := NewWithOptions(client, "test", Options{Context: ContextConfig{Enabled: true, MaxInputTokens: 4000, TargetInputTokens: tc.target, KeepLastTurns: 1}})
			history := mergeTaskHistory()
			history[len(history)-1]["content"] = "CONFLICT reader.rs " + strings.Repeat("conflict details ", 40)
			result, ok := app.summarizeMessages(context.Background(), history, tc.target)
			if !ok || latestUserMessageIndex(result) < 0 || len(client.optionMessages) != 1 {
				t.Fatalf("result=%#v ok=%t", result, ok)
			}
			input := buildSummaryInput(client.optionMessages[0])
			if !strings.Contains(input, "/model kiro/model") || strings.Contains(input, "CONFLICT reader.rs") != tc.hasTail {
				t.Fatalf("summary input = %s", input)
			}
		})
	}
}
