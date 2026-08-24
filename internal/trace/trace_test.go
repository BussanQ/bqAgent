package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type categorizedTestError struct{}

func (categorizedTestError) Error() string         { return "provider failed" }
func (categorizedTestError) ErrorCategory() string { return "model_rate_limit" }

func TestRecorderPersistsRedactedRunAndFeedback(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	recorder, err := store.Create("session-1", "turn-1", "", "agent", "model", "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	recorder.ToolCall("demo", map[string]any{"api_key": "secret", "path": "README.md"}, "large result", 0, nil)
	if err := recorder.Finish("done", nil); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Load(recorder.RunID())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusCompleted || meta.FinalSummary != "done" {
		t.Fatalf("meta=%+v", meta)
	}
	events, err := os.ReadFile(filepath.Join(root, ".agent", "runs", recorder.RunID(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), "secret") || !strings.Contains(string(events), "[REDACTED]") {
		t.Fatalf("events not redacted: %s", events)
	}
	if _, err := store.AddFeedback(recorder.RunID(), "up", "helpful", "test"); err != nil {
		t.Fatal(err)
	}
}

func TestRecorderPersistsModelRecoveryMetadata(t *testing.T) {
	root := t.TempDir()
	recorder, err := NewStore(root).Create("session-1", "turn-1", "", "agent", "model", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	recorder.ModelCallWithMetadata("context", TokenUsage{TotalTokens: 3}, time.Second, nil, ModelCallMetadata{
		RetryCount: 1, ReasoningDowngraded: true, ReasoningDowngradeSource: "provider_rejection",
		Recoveries: []ModelRecovery{
			{Kind: "provider_retry", Attempt: 2, Category: "model_server", StatusCode: 503, DelayMS: 10},
			{Kind: "reasoning_downgrade", Attempt: 3, Category: "model_request", StatusCode: 400},
		},
	})
	meta, err := recorder.store.Load(recorder.RunID())
	if err != nil {
		t.Fatal(err)
	}
	if meta.RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", meta.RetryCount)
	}
	events, err := os.ReadFile(filepath.Join(root, ".agent", "runs", recorder.RunID(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(events)
	for _, want := range []string{`"type":"model_call"`, `"reasoning_downgraded":true`, `"type":"provider_retry"`, `"type":"reasoning_downgrade"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("events missing %q: %s", want, text)
		}
	}
	if got := ClassifyError(categorizedTestError{}); got != "model_rate_limit" {
		t.Fatalf("ClassifyError = %q, want model_rate_limit", got)
	}
}
