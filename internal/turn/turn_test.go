package turn

import "testing"

func TestMessagesIgnoreReplayStaleUpdatesAndOtherTurns(t *testing.T) {
	var messages Messages
	first := Event{TurnID: "t", ItemID: "one", Seq: 1, Kind: "message.updated", Text: "draft"}
	final := Event{TurnID: "t", ItemID: "one", Seq: 2, Kind: "message.completed", Phase: "final_answer", Text: "answer"}
	if !messages.Apply(first) || !messages.Apply(final) {
		t.Fatal("initial events rejected")
	}
	late := first
	late.Seq = 3
	other := first
	other.TurnID = "other"
	other.Seq = 4
	input := Event{TurnID: "t", ItemID: "input", Seq: 5, Kind: "input.applied", Text: "user correction"}
	for _, e := range []Event{first, final, late, other, input} {
		if messages.Apply(e) {
			t.Fatalf("accepted stale event %+v", e)
		}
	}
	if len(messages.Items) != 1 || messages.Latest.Text != "answer" {
		t.Fatalf("projection=%+v", messages)
	}
}
