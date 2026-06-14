package agent

import (
	"context"
	"strings"
	"testing"

	"bqagent/internal/tools"
)

// capturingRecorder records every message handed to RecordMessage and also
// implements ContextCheckpointRecorder so the summarize path exercises the
// checkpoint save. It lets tests prove the synthetic summary is written to the
// checkpoint but never to the raw transcript.
type capturingRecorder struct {
	recorded   []map[string]any
	checkpoint string
}

func (r *capturingRecorder) RecordMessage(message map[string]any) error {
	r.recorded = append(r.recorded, message)
	return nil
}

func (r *capturingRecorder) SaveCheckpointSummary(summary string, _ []map[string]any, _ string) error {
	r.checkpoint = summary
	return nil
}

func overBudgetMessages() []map[string]any {
	return []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": strings.Repeat("older-user-", 20)},
		{"role": "assistant", "content": strings.Repeat("older-assistant-", 20)},
		{"role": "user", "content": "recent"},
	}
}

// When summarization fires, the loop adopts the compacted set as its working
// history and continues on it — so a multi-iteration over-budget run summarizes
// exactly once instead of re-summarizing the full history every turn. The
// synthetic summary must reach the checkpoint, never the transcript.
func TestSummarizationAdoptedIntoWorkingSet(t *testing.T) {
	client := &stubClient{
		responses: []AssistantMessage{
			{ToolCalls: []ToolCall{{ID: "call-1", Function: FunctionCall{Name: "noop", Arguments: `{}`}}}},
			{Role: "assistant", Content: "done"},
		},
		optionResponses: []AssistantMessage{{Role: "assistant", Content: "short"}},
	}
	recorder := &capturingRecorder{}
	app := NewWithOptions(client, "", Options{
		Recorder: recorder,
		Functions: map[string]tools.Function{
			"noop": func(_ context.Context, _ map[string]any) (string, error) { return "ok", nil },
		},
		Context: ContextConfig{
			Enabled:              true,
			MaxInputTokens:       200,
			TargetInputTokens:    40,
			KeepLastTurns:        1,
			SummarizationEnabled: true,
			SummaryTriggerTokens: 30,
		},
	})

	result, _, err := app.RunConversationTurn(context.Background(), overBudgetMessages(), 5)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if result != "done" {
		t.Fatalf("result = %q, want %q", result, "done")
	}

	// Compact once, then continue on the compacted context: only a single summary call
	// even though both iterations would individually be over the original budget.
	if len(client.optionMessages) != 1 {
		t.Fatalf("summary call count = %d, want 1 (compact once, adopt, continue)", len(client.optionMessages))
	}

	// Second model request must be built on the compacted base, not the full history.
	if len(client.messages) != 2 {
		t.Fatalf("model request count = %d, want 2", len(client.messages))
	}
	second := client.messages[1]
	foundSummary := false
	for _, message := range second {
		if content, _ := message["content"].(string); strings.Contains(content, EarlierConversationSummaryPrefix) {
			foundSummary = true
		}
		if content, _ := message["content"].(string); strings.Contains(content, strings.Repeat("older-user-", 20)) {
			t.Fatalf("second request still carries full pre-compaction history: %#v", message)
		}
	}
	if !foundSummary {
		t.Fatalf("second request missing the adopted summary message: %#v", second)
	}

	// The synthetic summary belongs to the checkpoint, never the raw transcript.
	if !strings.Contains(recorder.checkpoint, "short") {
		t.Fatalf("checkpoint summary = %q, want it to contain the generated summary", recorder.checkpoint)
	}
	for _, message := range recorder.recorded {
		if content, _ := message["content"].(string); strings.Contains(content, EarlierConversationSummaryPrefix) {
			t.Fatalf("synthetic summary leaked into the recorded transcript: %#v", message)
		}
	}
}

// Under budget, buildRequestMessages reports no compaction: no summary call fires
// and the working set is left untouched (regression guard for the nil-compacted path).
func TestNoCompactionWhenUnderBudget(t *testing.T) {
	client := &stubClient{
		responses:       []AssistantMessage{{Role: "assistant", Content: "hi"}},
		optionResponses: []AssistantMessage{{Role: "assistant", Content: "unexpected summary"}},
	}
	app := NewWithOptions(client, "", Options{
		Context: ContextConfig{
			Enabled:              true,
			MaxInputTokens:       200,
			TargetInputTokens:    100,
			KeepLastTurns:        4,
			SummarizationEnabled: true,
			SummaryTriggerTokens: 80,
		},
	})

	messages := []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "hello"},
	}
	result, _, err := app.RunConversationTurn(context.Background(), messages, 5)
	if err != nil {
		t.Fatalf("RunConversationTurn returned error: %v", err)
	}
	if result != "hi" {
		t.Fatalf("result = %q, want %q", result, "hi")
	}
	if len(client.optionMessages) != 0 {
		t.Fatalf("summary call count = %d, want 0 when under budget", len(client.optionMessages))
	}
}
