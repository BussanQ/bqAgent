package trace

import (
	"bqagent/internal/turn"
	"context"
	"testing"
)

func TestTracePreservesTurnStopOutcome(t *testing.T) {
	for _, status := range []turn.Status{turn.BudgetLimited, turn.Blocked, turn.Interrupted} {
		t.Run(string(status), func(t *testing.T) {
			store := NewStore(t.TempDir())
			recorder, err := store.Create("s", "t", "", "agent", "test", "prompt")
			if err != nil {
				t.Fatal(err)
			}
			outcome := turn.Outcome{}
			outcome.Set(status, "limit")
			var runErr error
			if status == turn.Interrupted {
				runErr = context.Canceled
			}
			if err = recorder.FinishTurn("checkpoint", runErr, outcome); err != nil {
				t.Fatal(err)
			}
			meta, err := store.Load(recorder.RunID())
			if err != nil || meta.Outcome == nil || meta.Outcome.Status != status || string(meta.Status) != string(status) {
				t.Fatalf("meta=%+v err=%v", meta, err)
			}
		})
	}
}
