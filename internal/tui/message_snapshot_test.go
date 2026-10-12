package tui

import (
	"strings"
	"testing"

	"bqagent/internal/turn"
	tea "charm.land/bubbletea/v2"
)

func TestMessageSnapshotsNeverAppendOrPrintOnToolToggle(t *testing.T) {
	model := toolDrawerModel(t, 2)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	events := []turn.Event{
		{TurnID: "t", ItemID: "a", Seq: 1, Kind: "message.updated", Phase: "draft", Text: "old"},
		{TurnID: "t", ItemID: "a", Seq: 2, Kind: "message.completed", Phase: "commentary", Text: "checking"},
		{TurnID: "t", ItemID: "b", Seq: 3, Kind: "message.updated", Phase: "draft", Text: "draft"},
		{TurnID: "t", ItemID: "b", Seq: 4, Kind: "message.completed", Phase: "final_answer", Text: "最终结论"},
	}
	for _, e := range events {
		model.handleTurnEvent(turnEvent{sequence: 1, kind: "message", message: e})
		model.handleTurnEvent(turnEvent{sequence: 1, kind: "message", message: e})
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "message", message: events[0]})
	if len(model.messages.Items) != 2 || model.stream != "最终结论" || len(model.printQueue) != 0 {
		t.Fatalf("stream=%q records=%d prints=%v", model.stream, len(model.messages.Items), model.printQueue)
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "最终结论", Outcome: turn.Outcome{Status: turn.Completed}}})
	for i := 0; i < 4; i++ {
		updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		model = updated.(Model)
		if strings.Count(model.View().Content, "最终结论") != 1 {
			t.Fatal("final answer lost or duplicated")
		}
	}
	model.commitTurn()
	model.commitTurn()
	if strings.Count(strings.Join(model.printQueue, "\n"), "最终结论") != 1 {
		t.Fatal("final snapshot printed twice")
	}
}

func TestTurnOutcomeNoticeComesFromBackend(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	outcome := turn.Outcome{}
	outcome.Set(turn.BudgetLimited, "max_iterations")
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "已完成分析，尚未修改", Outcome: outcome}})
	if !strings.Contains(model.View().Content, outcome.Notice) {
		t.Fatalf("missing backend stop notice: %s", model.View().Content)
	}
}
