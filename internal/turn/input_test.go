package turn

import (
	"errors"
	"sync"
	"testing"
)

func TestInputQueueOrdersCorrectionsBeforeFollowUps(t *testing.T) {
	q := &InputQueue{}
	for _, input := range []Input{{ID: "later1", Kind: FollowUp, Text: "later one"}, {ID: "now1", Kind: Steering, Text: "correct one"}, {ID: "now2", Kind: Steering, Text: "correct two"}, {ID: "later2", Kind: FollowUp, Text: "later two"}} {
		if err := q.Add(input); err != nil {
			t.Fatal(err)
		}
	}
	inputs := q.TakeSteering()
	if len(inputs) != 2 || inputs[0].ID != "now1" || inputs[1].ID != "now2" {
		t.Fatalf("corrections=%+v", inputs)
	}
	for _, id := range []string{"later1", "later2"} {
		inputs = q.Finish()
		if len(inputs) != 1 || inputs[0].ID != id {
			t.Fatalf("followup=%+v", inputs)
		}
	}
	if len(q.Finish()) != 0 || !errors.Is(q.Add(Input{Kind: Steering, Text: "late"}), ErrInputClosed) {
		t.Fatal("finished queue accepts input")
	}
}

func TestInputQueueFinishCannotLoseAcceptedInput(t *testing.T) {
	for i := 0; i < 100; i++ {
		q := &InputQueue{}
		var wg sync.WaitGroup
		var addErr error
		var taken []Input
		wg.Add(2)
		go func() { defer wg.Done(); addErr = q.Add(Input{Kind: Steering, Text: "correction"}) }()
		go func() { defer wg.Done(); taken = q.Finish() }()
		wg.Wait()
		if addErr == nil && len(taken) != 1 {
			t.Fatal("accepted correction disappeared at finish")
		}
		if addErr != nil && !errors.Is(addErr, ErrInputClosed) {
			t.Fatal(addErr)
		}
	}
}

func TestInputQueueClosePreservesUnconsumedMessages(t *testing.T) {
	q := &InputQueue{}
	_ = q.Add(Input{Kind: Steering, Text: "first"})
	_ = q.Add(Input{Kind: FollowUp, Text: "second"})
	for i := 0; i < 2; i++ {
		pending := q.Close()
		if len(pending) != 2 || pending[0].Text != "first" || pending[1].Text != "second" {
			t.Fatalf("pending=%+v", pending)
		}
	}
}
