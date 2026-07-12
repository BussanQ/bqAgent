package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
