package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCompletedToolInspectorRestoresTranscriptAndDraft(t *testing.T) {
	for _, count := range []int{1, 4, 10} {
		for _, long := range []bool{false, true} {
			t.Run(fmt.Sprintf("tools=%d/long=%t", count, long), func(t *testing.T) {
				base := toolDrawerModel(t, count)
				base.tools.add(ToolEvent{Kind: "tool_result", ID: "call-1", Name: "read_file", Status: "succeeded", Preview: "tool-preview"})
				base.sequence = 1
				base.active = &activeTurn{sequence: 1, cancel: func() {}}
				answer := "answer-start\n\nanswer-end"
				if long {
					answer = "answer-start\n\n" + strings.Repeat("answer-body\n\n", 50) + "answer-end"
				}
				base.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: answer}})
				history := strings.Join(base.printQueue, "\n")
				base.printQueue = nil // Already inserted into the native transcript.
				base.input.setValue("unsent draft")
				original, reply := base.View(), base.reply
				var model tea.Model = newInlineModel(base)
				for i := 0; i < 6; i++ {
					updated, cmd := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
					model = updated
					if cmd != nil || !model.View().AltScreen || !strings.Contains(model.View().Content, "tool-preview") || strings.Contains(model.View().Content, "answer-end") {
						t.Fatal("completed tools must open separately without modifying history")
					}
					for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.KeyPressMsg{Code: tea.KeyEnter}, tea.PasteMsg{Content: "accidental paste"}, tea.WindowSizeMsg{Width: 32, Height: 8}, tea.WindowSizeMsg{Width: 72, Height: 24}} {
						model, cmd = model.Update(msg)
						inner := model.(inlineModel).model.(Model)
						if cmd != nil || inner.reply != reply || inner.input.value() != "unsent draft" || inner.historyPrints != 0 || len(inner.historyQueue) != 0 {
							t.Fatal("inspector interaction changed the answer, draft, or history")
						}
						assertLiveFrameFits(t, inner)
					}
					var close tea.Msg = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
					if i%3 == 1 {
						close = tea.KeyPressMsg{Code: tea.KeyEscape}
					} else if i%3 == 2 {
						close = tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 0}
					}
					model, cmd = model.Update(close)
					view := model.View()
					if cmd != nil || view.AltScreen || view.Content != original.Content || view.Cursor == nil || view.Cursor.Position != original.Cursor.Position {
						t.Fatal("closing inspector moved or changed the original conclusion")
					}
				}
				inner := model.(inlineModel).model.(Model)
				inner.commitTurn()
				text := history + "\n" + strings.Join(inner.printQueue, "\n")
				if strings.Count(text, "工具活动") != 1 || strings.Count(text, "answer-start") != 1 || strings.Count(text, "answer-end") != 1 {
					t.Fatal("inspecting then committing lost or duplicated the transcript")
				}
			})
		}
	}
}

func TestCompletedToolInspectorWaitsForHistory(t *testing.T) {
	base := toolDrawerModel(t, 10)
	base.sequence = 1
	base.active = &activeTurn{sequence: 1, cancel: func() {}}
	var model tea.Model = newInlineModel(base)
	model, _ = model.Update(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: strings.Repeat("answer-body\n\n", 50) + "answer-end"}})
	inner := model.(inlineModel).model.(Model)
	if inner.historyPrints != 1 {
		t.Fatal("test must have a pending history insertion")
	}
	model, cmd := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if cmd != nil || model.View().AltScreen {
		t.Fatal("must keep the main screen until history printing finishes")
	}
	model, _ = model.Update(historyPrintedMsg{})
	if !model.View().AltScreen {
		t.Fatal("pending inspector did not open after history acknowledgement")
	}
	model, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.View().AltScreen || !strings.Contains(model.View().Content, "answer-end") {
		t.Fatal("closing pending inspector lost the conclusion")
	}
}

func TestCompletedToolInspectorExitsBeforeResizeHistoryOrQuit(t *testing.T) {
	for _, quit := range []bool{false, true} {
		t.Run(fmt.Sprint(quit), func(t *testing.T) {
			base := toolDrawerModel(t, 10)
			base.reply = "answer-start\n\nanswer-middle\n\nanswer-end"
			var model tea.Model = newInlineModel(base)
			model, _ = model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			if !model.View().AltScreen {
				t.Fatal("inspector did not open")
			}
			model, _ = model.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
			key := tea.KeyPressMsg{Code: tea.KeyEscape}
			if quit {
				key = tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}
			}
			model, cmd := model.Update(key)
			inner := model.(inlineModel).model.(Model)
			if cmd == nil || inner.historyPrints != 1 || model.View().AltScreen {
				t.Fatal("resize/quit history must target the main screen")
			}
			assertLiveFrameFits(t, inner)
		})
	}
}
