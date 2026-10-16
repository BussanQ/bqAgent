package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestTurnCompletionRestoresNativeMouseScrolling(t *testing.T) {
	for _, count := range []int{1, 6} {
		for _, expanded := range []bool{false, true} {
			for _, outcome := range []string{"reply", "stream_fallback", "cancelled", "context_cancelled", "failed"} {
				t.Run(fmt.Sprintf("tools_%d/expanded_%t/%s", count, expanded, outcome), func(t *testing.T) {
					model := toolDrawerModel(t, 0)
					model.height = 60
					model.resizeInput()
					model.sequence = 1
					model.active = &activeTurn{sequence: 1, events: make(chan turnEvent), cancel: func() {}}
					update := func(message tea.Msg) {
						updated, _ := model.Update(message)
						model = updated.(Model)
					}
					for i := 1; i <= count; i++ {
						id := fmt.Sprint(i)
						update(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{Kind: "tool_start", ID: id, Name: "read_file"}})
						preview := ""
						if count == 1 {
							preview = "tool output"
						}
						update(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{Kind: "tool_result", ID: id, Name: "read_file", Status: "succeeded", Preview: preview}})
					}
					if model.View().MouseMode != tea.MouseModeCellMotion {
						t.Fatal("running tools should retain mouse interaction")
					}
					if expanded {
						update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
					}
					update(turnEvent{sequence: 1, kind: "token", text: "short response"})
					model.input.setValue("unsent draft")
					done := turnEvent{sequence: 1, kind: "done"}
					switch outcome {
					case "reply":
						done.result.Reply = "short response"
					case "cancelled":
						model.active.cancelled = true
					case "context_cancelled":
						done.err = context.Canceled
					case "failed":
						done.err = errors.New("test turn failure")
					}
					update(done)
					if model.tools.committed {
						t.Fatal("test must exercise unarchived tools")
					}
					if model.toolMouse || model.tools.expanded || model.View().MouseMode != tea.MouseModeNone {
						t.Fatal("completed turn must release mouse tracking and collapse the tool drawer")
					}
					original := model.View().Content
					for _, close := range []tea.Msg{
						tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl},
						tea.KeyPressMsg{Code: tea.KeyEscape},
						tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 0},
					} {
						update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
						if !model.View().AltScreen || model.View().MouseMode != tea.MouseModeCellMotion {
							t.Fatal("completed tools should remain inspectable with mouse scrolling")
						}
						update(close)
						view := model.View()
						if view.AltScreen || view.MouseMode != tea.MouseModeNone || view.Content != original || model.input.value() != "unsent draft" {
							t.Fatal("leaving inspector must restore native scrolling, conversation and draft")
						}
					}
				})
			}
		}
	}
}
