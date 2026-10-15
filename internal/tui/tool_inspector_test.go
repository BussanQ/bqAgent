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
					if cmd != nil || !model.View().AltScreen || !strings.Contains(model.View().Content, "file-001.go") || strings.Contains(model.View().Content, "answer-end") {
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
					model, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape}) // Detail back to list.
					if !model.View().AltScreen {
						t.Fatal("leaving one tool must retain the tool list")
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

func TestToolInspectorSelectsAndScrollsSingleTool(t *testing.T) {
	model := toolDrawerModel(t, 80)
	model.reply = "final answer"
	model.input.setValue("unsent draft")
	const tail = "唯一输出尾部"
	output := strings.Repeat("输出正文\n", 100) + tail
	model.tools.add(ToolEvent{Kind: "tool_result", ID: "call-80", Name: "read_file", Status: "succeeded", Preview: output})
	model.tools.items[79].start.Arguments = map[string]any{"a": 1, "b": 2, "c": 3, "fourth": "超过摘要限制的参数"}
	original := model.View().Content
	update := func(message tea.Msg) {
		t.Helper()
		updated, cmd := model.Update(message)
		model = updated.(Model)
		if cmd != nil || len(model.historyQueue) != 0 || model.historyPrints != 0 || model.input.value() != "unsent draft" || model.reply != "final answer" {
			t.Fatal("inspection modified draft, reply, or transcript")
		}
		assertLiveFrameFits(t, model)
	}
	update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if strings.Contains(model.View().Content, "输出正文") {
		t.Fatal("list must not be filled by a tool's output")
	}
	update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if model.inspection.selected != 79 || !strings.Contains(model.View().Content, "#80") {
		t.Fatal("last tool is unreachable")
	}
	list := model.View().Content
	update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !model.inspection.detail || !strings.Contains(model.View().Content, "超过摘要限制的参数") || strings.Contains(model.View().Content, tail) {
		t.Fatal("selected tool detail or complete arguments are missing")
	}
	update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if model.inspection.offset == 0 {
		t.Fatal("page down did not scroll")
	}
	pageOffset := model.inspection.offset
	update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if model.inspection.offset != pageOffset+1 {
		t.Fatal("mouse wheel did not scroll detail")
	}
	update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if model.inspection.offset != pageOffset {
		t.Fatal("mouse wheel up did not restore detail position")
	}
	update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(model.View().Content, tail) {
		t.Fatal("tool output tail is unreachable")
	}
	for _, size := range [][2]int{{18, 9}, {8, 5}, {72, 24}} {
		update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
	}
	update(tea.KeyPressMsg{Code: tea.KeyHome})
	if model.inspection.offset != 0 || !strings.Contains(model.View().Content, "#80 read_file") {
		t.Fatal("home did not restore detail heading")
	}
	update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.inspection.detail || model.View().Content != list {
		t.Fatal("returning to list lost its selection or position")
	}
	update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if model.View().AltScreen || model.View().Content != original || model.tools.items[79].result.Preview != output {
		t.Fatal("returning from inspector changed transcript or stored output")
	}
}

func TestCompletedToolInspectorOpensWithoutOutput(t *testing.T) {
	model := toolDrawerModel(t, 1)
	model.reply = "answer"
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	model = updated.(Model)
	if !model.View().AltScreen {
		t.Fatal("completed tool arguments must remain inspectable without output")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if !strings.Contains(model.View().Content, "file-001.go") || !strings.Contains(model.View().Content, "无输出") {
		t.Fatal("tool parameters or missing-output explanation are absent")
	}
}

func TestToolInspectorRepeatedNavigationDoesNotNest(t *testing.T) {
	base := toolDrawerModel(t, 12)
	base.reply = "answer"
	base.input.setValue("draft")
	base.tools.add(ToolEvent{Kind: "tool_result", ID: "call-1", Name: "read_file", Status: "succeeded", Preview: strings.Repeat("output\n", 100) + "tail"})
	var model tea.Model = newInlineModel(base)
	original := model.View().Content
	update := func(message tea.Msg) {
		t.Helper()
		var command tea.Cmd
		model, command = model.Update(message)
		if command != nil {
			t.Fatal("navigation unexpectedly scheduled terminal or history work")
		}
		inner := model.(inlineModel).model.(Model)
		if model.View().Content != inner.View().Content {
			t.Fatal("wrapper retained a stale screen")
		}
		assertLiveFrameFits(t, inner)
	}
	for i := 0; i < 20; i++ {
		update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		update(tea.KeyPressMsg{Code: tea.KeyEnter})
		for j := 0; j < 5; j++ {
			update(tea.KeyPressMsg{Code: tea.KeyEnter})
			update(tea.KeyPressMsg{Code: tea.KeyRight})
		}
		update(tea.KeyPressMsg{Code: tea.KeyDown})
		if model.(inlineModel).model.(Model).inspection.offset != 1 {
			t.Fatal("down arrow did not scroll detail")
		}
		update(tea.KeyPressMsg{Code: tea.KeyUp})
		if model.(inlineModel).model.(Model).inspection.offset != 0 {
			t.Fatal("up arrow did not scroll detail")
		}
		update(tea.KeyPressMsg{Code: tea.KeyEnd})
		if !strings.Contains(model.View().Content, "tail") {
			t.Fatal("output tail was unreachable")
		}
		update(tea.KeyPressMsg{Code: tea.KeyEscape})
		inner := model.(inlineModel).model.(Model)
		if !inner.toolInspector || inner.inspection.detail {
			t.Fatal("one escape must return to list regardless of repeated enter")
		}
		update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if model.View().AltScreen || model.View().Content != original {
			t.Fatal("second escape did not restore conversation")
		}
	}
}

func TestToolInspectorMouseOpensCorrectTool(t *testing.T) {
	model := toolDrawerModel(t, 4)
	model.reply = "answer"
	model.tools.add(ToolEvent{Kind: "tool_result", ID: "call-2", Name: "read_file", Status: "failed", Preview: "second tool output", Truncated: true})
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	model = updated.(Model)
	// The exit-confirmation notice adds a row before the clickable list.
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	model = updated.(Model)
	y := len(model.toolInspectorHeader()) + 1
	updated, _ = model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: y})
	model = updated.(Model)
	if model.inspection.selected != 1 || !model.inspection.detail || !strings.Contains(model.View().Content, "second tool output") || !strings.Contains(model.View().Content, "输出已截断") {
		t.Fatalf("click opened wrong tool or lost truncation notice: %s", model.View().Content)
	}
	updated, _ = model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 0})
	model = updated.(Model)
	if !model.toolInspector || model.inspection.detail || model.inspection.selected != 1 {
		t.Fatal("detail header must return to the selected list item")
	}
}

func TestToolInspectorDetailHandlesMissingOutputAndResize(t *testing.T) {
	for _, finished := range []bool{false, true} {
		model := toolDrawerModel(t, 1)
		model.tools.items[0].hasResult = finished
		model.reply = "answer"
		model.tools.committed = true
		model.toggleToolGroup()
		model.handleToolInspectorKey("enter")
		want := "等待工具完成"
		if finished {
			want = "无输出"
		}
		if !strings.Contains(model.View().Content, want) {
			t.Fatal("empty or running output has no explanation")
		}
		for _, size := range [][2]int{{8, 2}, {18, 3}, {18, 9}, {72, 24}} {
			updated, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			model = updated.(Model)
			assertLiveFrameFits(t, model)
			if size[1] == 3 && model.toolInspectorHeight() < 1 {
				t.Fatal("short terminal must retain a scrollable content row")
			}
		}
		model.discardToolGroup()
		if model.toolInspector || model.inspection.detail || model.inspection.cache != nil {
			t.Fatal("discard retained inspection state")
		}
	}
}

func TestPendingToolInspectorDoesNotCaptureInvisibleInput(t *testing.T) {
	for _, message := range []tea.Msg{tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.PasteMsg{Content: "x"}, tea.KeyPressMsg{Code: tea.KeyEscape}} {
		model := toolDrawerModel(t, 10)
		model.reply = "answer"
		model.historyPrints = 1
		updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		model = updated.(Model)
		if model.toolInspector || !model.inspectorPending || model.View().AltScreen {
			t.Fatal("pending history must not activate an invisible inspector")
		}
		updated, _ = model.Update(message)
		model = updated.(Model)
		if model.inspectorPending || model.toolInspector {
			t.Fatal("editing or escape must cancel pending inspection")
		}
		if _, escape := message.(tea.KeyPressMsg); !escape || message.(tea.KeyPressMsg).Code != tea.KeyEscape {
			if model.input.value() != "x" {
				t.Fatal("pending inspection swallowed draft input")
			}
		}
		updated, _ = model.Update(historyPrintedMsg{})
		if updated.(Model).View().AltScreen {
			t.Fatal("cancelled inspection opened after history completed")
		}
	}
}

func TestFirstToolExpansionOnlyShowsSummaryRows(t *testing.T) {
	for _, running := range []bool{false, true} {
		model := toolDrawerModel(t, 6)
		if running {
			model.active = &activeTurn{sequence: 1, cancel: func() {}}
		}
		model.tools.add(ToolEvent{Kind: "tool_result", ID: "call-1", Name: "read_file", Status: "failed", Preview: strings.Repeat("failure-output\n", 100)})
		model.tools.add(ToolEvent{Kind: "tool_start", ID: "call-2", Seq: 2, Name: "todo_write", Arguments: map[string]any{"todos": `[{"content":"task","status":"pending"}]`}})
		model.tools.add(ToolEvent{Kind: "tool_result", ID: "call-2", Name: "todo_write", Status: "succeeded", Preview: "todo-output"})
		updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		model = updated.(Model)
		lines := model.tools.viewLines(model.styles, model.contentWidth())
		if len(lines) != 7 || strings.Contains(strings.Join(lines, "\n"), "failure-output") || strings.Contains(strings.Join(lines, "\n"), "todo-output") {
			t.Fatalf("running=%t: first expansion contains tool bodies: %q", running, lines)
		}
		assertLiveFrameFits(t, model)
	}
}

func TestRunningToolDrawerWheelReachesOutputTail(t *testing.T) {
	model := toolDrawerModel(t, 6)
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	model.tools.add(ToolEvent{Kind: "tool_result", ID: "call-6", Name: "read_file", Status: "succeeded", Preview: strings.Repeat("body\n", 50) + "output-tail"})
	model.input.setValue("draft")
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	model = updated.(Model)
	list := model.View().Content
	if strings.Contains(list, "body") || strings.Contains(list, "output-tail") || len(model.tools.viewLines(model.styles, model.contentWidth())) != 7 {
		t.Fatal("first Ctrl+O must show exactly one summary row per tool")
	}
	headerY := model.height - len(strings.Split(list, "\n"))
	updated, _ = model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: headerY + 6})
	model = updated.(Model)
	if model.tools.detail != 6 {
		t.Fatal("click did not open the selected tool")
	}
	start := model.View().Content
	for i := 0; i < 30; i++ {
		updated, _ = model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		model = updated.(Model)
	}
	if !strings.Contains(model.View().Content, "output-tail") || !strings.Contains(model.View().Content, "工具详情") {
		t.Fatal("running tool drawer cannot scroll to output tail or lost its header")
	}
	for i := 0; i < 30; i++ {
		updated, _ = model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		model = updated.(Model)
	}
	if model.View().Content != start || model.input.value() != "draft" || model.View().AltScreen {
		t.Fatal("running drawer scrolling changed draft or failed to restore start")
	}
	headerY = model.height - len(strings.Split(model.View().Content, "\n"))
	updated, _ = model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: headerY})
	model = updated.(Model)
	if model.tools.detail != 0 || model.View().Content != list {
		t.Fatal("detail header did not restore the one-row list")
	}
}

func TestRunningToolListClickAfterScrollAndCompletion(t *testing.T) {
	model := toolDrawerModel(t, 80)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	model.toggleToolGroup()
	for i := 0; i < 30; i++ {
		updated, _ := model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		model = updated.(Model)
	}
	rows := strings.Split(model.View().Content, "\n")
	headerY := model.height - len(rows)
	lastRow := len(model.tools.viewport(model.styles, model.contentWidth(), model.toolViewportHeight())) - 1
	updated, _ := model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: headerY + lastRow})
	model = updated.(Model)
	if model.tools.detail != 80 || !strings.Contains(model.View().Content, "#80 read_file") {
		t.Fatal("scrolled list click opened the wrong tool")
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{Kind: "tool_result", ID: "call-80", Name: "read_file", Status: "succeeded", Preview: "updated-result"}})
	if !strings.Contains(model.View().Content, "updated-result") {
		t.Fatal("selected tool detail did not refresh after its result")
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "answer"}})
	if !strings.Contains(model.View().Content, "answer") || strings.Contains(model.View().Content, "updated-result") {
		t.Fatal("completion did not restore the conversation")
	}
	model.toggleToolGroup()
	if !model.View().AltScreen || model.inspection.detail || strings.Contains(model.View().Content, "updated-result") {
		t.Fatal("reopening completed tools must start with the summary list")
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
			if quit {
				model, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
