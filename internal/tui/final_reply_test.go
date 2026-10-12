package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestFinalReplyRemainsVisibleAfterTools(t *testing.T) {
	const final = "10 次 ls 均已成功执行。共 9 个条目，文件类型未核实。"
	for _, test := range []struct {
		name, stream, reply, want string
		streamed                  bool
	}{
		{name: "streamed final", stream: final, reply: final, want: final, streamed: true},
		{name: "final snapshot without tokens", reply: final, want: final, streamed: true},
		{name: "corrected closeout", stream: "7 个文件、2 个子目录。" + final, reply: final, want: final, streamed: true},
		{name: "partial stream", stream: "10 次 ls", reply: final, want: final, streamed: true},
		{name: "nonstreamed", reply: final, want: final},
		{name: "stream fallback", stream: final, want: final, streamed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := toolDrawerModel(t, 10)
			model.sequence = 1
			model.active = &activeTurn{sequence: 1, cancel: func() {}}
			// Prior commentary is not evidence that the final answer was shown.
			model.liveRunes = 100
			model.stream = test.stream
			updated, _ := model.Update(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: test.reply, Streamed: test.streamed}})
			model = updated.(Model)
			for i := 0; i < 5; i++ {
				content := model.View().Content
				if strings.Count(content, test.want) != 1 || strings.Contains(content, "7 个文件") {
					t.Fatalf("final conclusion missing, stale or duplicated: %q", content)
				}
				if strings.Index(content, "工具活动") > strings.Index(content, test.want) {
					t.Fatal("conclusion must follow tool activity")
				}
				updated, command := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
				model = updated.(Model)
				if command != nil {
					t.Fatal("toggling tools must not reprint the conclusion")
				}
				assertLiveFrameFits(t, model)
			}
		})
	}
}

func TestFinalReplyCommitsOnceBeforeNextTurnOrExit(t *testing.T) {
	for _, action := range []string{"next turn", "queued turn", "exit", "clear"} {
		t.Run(action, func(t *testing.T) {
			model := toolDrawerModel(t, 10)
			model.sequence = 1
			model.active = &activeTurn{sequence: 1, cancel: func() {}}
			if action == "queued turn" {
				model.queued = &queuedPrompt{value: "下一轮"}
			}
			model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "最终结论"}})
			switch action {
			case "next turn":
				model.submit("下一轮")
			case "exit":
				model.requestQuit()
			case "clear":
				model.handleKey(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
				if strings.Contains(model.View().Content, "最终结论") || strings.Contains(strings.Join(model.printQueue, "\n"), "最终结论") {
					t.Fatal("clear retained the previous conclusion")
				}
				return
			}
			// Repeating a commit/exit cannot duplicate either tools or reply.
			model.requestQuit()
			printed := strings.Join(model.printQueue, "\n")
			if strings.Count(printed, "最终结论") != 1 || strings.Count(printed, "工具活动") != 1 {
				t.Fatalf("completed turn lost or duplicated: %q", printed)
			}
			if strings.Index(printed, "工具活动") > strings.Index(printed, "最终结论") {
				t.Fatal("history reordered tools and final reply")
			}
			if action != "exit" && strings.Index(printed, "最终结论") > strings.Index(printed, "下一轮") {
				t.Fatal("final conclusion was printed after the next prompt")
			}
			if strings.Contains(model.View().Content, "最终结论") {
				t.Fatal("committed reply remained in the live frame")
			}
		})
	}
}

func TestLongFinalReplyPrintsWholeConclusionWithoutPaging(t *testing.T) {
	model := toolDrawerModel(t, 10)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf("结论第%03d行", i))
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: strings.Join(lines, "\n\n")}})
	prefix := strings.Join(model.printQueue, "\n")
	printed := prefix + "\n" + model.View().Content
	for _, line := range lines {
		if strings.Count(printed, line) != 1 {
			t.Fatalf("completion did not immediately print %q exactly once", line)
		}
	}
	if model.reply == "" || !strings.Contains(model.View().Content, "结论第060行") || len(model.tools.items) != 10 || model.tools.expanded {
		t.Fatal("completion hid the conclusion or changed the tool drawer")
	}
	model.printQueue = nil // History has been emitted by Update.
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}, {Code: 'o', Mod: tea.ModCtrl}, {Code: 'o', Mod: tea.ModCtrl}} {
		updated, cmd := model.Update(key)
		model = updated.(Model)
		if cmd != nil {
			t.Fatal("paging/toggling tools reprinted the completed answer")
		}
		if !strings.Contains(model.View().Content, "结论第060行") {
			t.Fatal("tool interaction hid the conclusion")
		}
		assertLiveFrameFits(t, model)
	}
	model.commitTurn()
	model.commitTurn()
	printed = prefix + "\n" + strings.Join(model.printQueue, "\n")
	for _, line := range lines {
		if strings.Count(printed, line) != 1 {
			t.Fatalf("next turn lost or duplicated %q", line)
		}
	}
	if model.reply != "" {
		t.Fatal("committed suffix remained in the live frame")
	}

}

func TestReplyBecomingTooTallOnResizePrintsOnce(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	answer := "第一段\n\n第二段\n\n第三段"
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: answer}})
	if model.reply == "" || len(model.printQueue) != 0 {
		t.Fatal("short answer should be fully visible in the live area")
	}
	updated, command := model.Update(tea.WindowSizeMsg{Width: 18, Height: 6})
	model = updated.(Model)
	if command == nil || !strings.Contains(model.reply, "第三段") || model.historyPrints != 1 {
		t.Fatal("resize must preserve the conclusion and commit only overflow")
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
	model = updated.(Model)
	if !strings.Contains(model.reply, "第三段") || model.historyPrints != 1 {
		t.Fatal("growing the terminal reintroduced or reprinted the reply")
	}
}

func TestLongReplyPreservesRenderedSuffixOnResize(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		t.Run(fmt.Sprintf("noColor=%t", noColor), func(t *testing.T) {
			model := toolDrawerModel(t, 10)
			model.config.NoColor = noColor
			model.styles = newTheme(noColor)
			model.sequence = 1
			model.active = &activeTurn{sequence: 1, cancel: func() {}}
			answer := "## 结果\n\n```text\n"
			for i := 0; i < 30; i++ {
				answer += fmt.Sprintf("record-%02d\n", i)
			}
			answer += "```\n\n**最终结论**"
			want := ansi.Hardwrap(renderMarkdown(answer, model.contentWidth(), noColor), model.contentWidth(), false)
			model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: answer}})
			// The first history entry is the tool group, before the answer prefix.
			if !model.tools.committed || !strings.Contains(model.printQueue[0], "工具活动") {
				t.Fatal("tools were not committed before the answer")
			}
			combined := strings.Join(model.printQueue[1:], "\n") + "\n" + strings.Join(model.streamLines(model.contentWidth()), "\n")
			if combined != want {
				t.Fatal("splitting the answer changed the rendered Markdown")
			}
			for _, size := range [][2]int{{32, 10}, {72, 24}, {32, 10}} {
				model.width, model.height = size[0], size[1]
				model.resizeInput()
				model.flushOverflowReply()
				assertLiveFrameFits(t, model)
				if !strings.Contains(ansi.Strip(model.View().Content), "最终结论") {
					t.Fatal("resize removed the conclusion from the live frame")
				}
			}
			model.commitTurn()
			model.commitTurn()
			printed := ansi.Strip(strings.Join(model.printQueue, "\n"))
			for i := 0; i < 30; i++ {
				if strings.Count(printed, fmt.Sprintf("record-%02d", i)) != 1 {
					t.Fatalf("resize lost or duplicated code block line %d", i)
				}
			}
			if strings.Count(printed, "最终结论") != 1 {
				t.Fatal("resize lost or duplicated the conclusion")
			}
		})
	}
}

func TestLongReplyToolHistoryPrecedesAnswerAndRemainsInspectable(t *testing.T) {
	for _, count := range []int{4, 10} {
		for _, expanded := range []bool{false, true} {
			t.Run(fmt.Sprintf("tools=%d/expanded=%t", count, expanded), func(t *testing.T) {
				model := toolDrawerModel(t, count)
				model.tools.expanded = expanded
				model.sequence = 1
				model.active = &activeTurn{sequence: 1, cancel: func() {}}
				model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "answer-start\n\n" + strings.Repeat("answer-body\n\n", 50) + "answer-end"}})
				prefix := strings.Join(model.printQueue, "\n")
				if !model.tools.committed || strings.Index(prefix, "工具活动") >= strings.Index(prefix, "answer-start") {
					t.Fatal("history must commit tools before the answer prefix")
				}
				if strings.Contains(model.View().Content, "工具活动") || !strings.Contains(model.View().Content, "answer-end") {
					t.Fatal("live tools split the answer or hid its conclusion")
				}
				model.printQueue = nil
				for i := 0; i < 3; i++ {
					updated, command := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
					model = updated.(Model)
					view := model.View().Content
					if command != nil || !model.tools.expanded || !strings.Contains(view, "file-001.go") || strings.Index(view, "本轮工具详情") < strings.Index(view, "answer-end") {
						t.Fatal("Ctrl+O must open an inspector after the conclusion without printing history")
					}
					for _, key := range []tea.KeyPressMsg{{Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}} {
						updated, command = model.Update(key)
						model = updated.(Model)
						if command != nil {
							t.Fatal("inspector paging printed history")
						}
					}
					if i == 0 {
						updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					} else if i == 1 {
						lines := strings.Split(model.View().Content, "\n")
						y := 0
						for !strings.Contains(lines[y], "本轮工具详情") {
							y++
						}
						updated, command = model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: model.height - len(lines) + y})
					} else {
						updated, command = model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
					}
					model = updated.(Model)
					if command != nil || model.tools.expanded || model.toolMouse || strings.Contains(model.View().Content, "本轮工具详情") {
						t.Fatal("closing the inspector retained tool rows or printed history")
					}
					assertLiveFrameFits(t, model)
				}
				model.commitTurn()
				model.commitTurn()
				text := prefix + "\n" + strings.Join(model.printQueue, "\n")
				if strings.Count(text, "工具活动") != 1 || strings.Count(text, "answer-start") != 1 || strings.Count(text, "answer-body") != 50 || strings.Count(text, "answer-end") != 1 {
					t.Fatal("next turn lost or duplicated tools or answer")
				}
			})
		}
	}
}

func TestReplyResizeCommitsToolsBeforeFirstAnswerOverflow(t *testing.T) {
	model := toolDrawerModel(t, 10)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "answer-start\n\nanswer-middle\n\nanswer-end"}})
	if model.tools.committed || len(model.printQueue) != 0 {
		t.Fatal("fitting answer should keep its original interactive tool drawer")
	}
	for _, size := range [][2]int{{32, 8}, {72, 24}, {32, 8}} {
		model.width, model.height = size[0], size[1]
		model.resizeInput()
		model.flushOverflowReply()
		assertLiveFrameFits(t, model)
		text := strings.Join(model.printQueue, "\n") + "\n" + model.View().Content
		if strings.Count(text, "工具活动") != 1 || strings.Index(text, "工具活动") >= strings.Index(text, "answer-start") || strings.Count(text, "answer-end") != 1 {
			t.Fatal("resize reordered or duplicated the tools and answer")
		}
	}
	model.commitTurn()
	text := strings.Join(model.printQueue, "\n")
	if strings.Count(text, "工具活动") != 1 || strings.Count(text, "answer-start") != 1 || strings.Count(text, "answer-end") != 1 {
		t.Fatal("next turn reprinted the tools or lost the resized answer")
	}
}

func TestFinalReplyWithoutToolsCommitsOnce(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "直接回答"}})
	if !strings.Contains(model.View().Content, "直接回答") {
		t.Fatal("answer without tools is missing")
	}
	model.commitTurn()
	model.commitTurn()
	if strings.Count(strings.Join(model.printQueue, "\n"), "直接回答") != 1 {
		t.Fatal("answer without tools was lost or duplicated on commit")
	}
}

func TestFinalReplySurvivesInlineCompletionRepaint(t *testing.T) {
	base := toolDrawerModel(t, 10)
	base.sequence = 1
	base.active = &activeTurn{sequence: 1, cancel: func() {}}
	base.stream = strings.Repeat("先前的草稿\n", 40)
	var model tea.Model = newInlineModel(base)
	probe := newInlineRendererProbe(base.width)
	probe.draw(t, model.View())
	model, _ = model.Update(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "最终结论：10 次 ls 均已成功。", Streamed: true}})
	probe.draw(t, model.View())
	probe.renderer.MoveTo(0, 0)
	probe.renderer.Erase()
	model, _ = model.Update(tea.ClearScreen())
	probe.draw(t, model.View())
	model, _ = model.Update(inlineRepaintMsg{})
	probe.draw(t, model.View())
	if !strings.Contains(ansi.Strip(probe.lastOutput), "最终结论：10 次 ls 均已成功。") {
		t.Fatalf("renderer did not paint the final answer: %q", probe.lastOutput)
	}
	if !strings.Contains(model.View().Content, "最终结论：10 次 ls 均已成功。") || strings.Contains(model.View().Content, "先前的草稿") {
		t.Fatalf("completion repaint hid the final answer: %q", model.View().Content)
	}
}

func TestLanguageAnswerAppearsInTerminalOutput(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		for _, size := range [][2]int{{72, 24}, {48, 12}} {
			t.Run(fmt.Sprintf("noColor=%t/size=%v", noColor, size), func(t *testing.T) {
				model := toolDrawerModel(t, 4)
				model.width, model.height = size[0], size[1]
				model.config.NoColor = noColor
				model.styles = newTheme(noColor)
				model.resizeInput()
				model.sequence = 1
				model.active = &activeTurn{sequence: 1, cancel: func() {}}
				answer := "主要是 **Shell/Bash**，辅助语言是 **Python**。\n\n" + strings.Repeat("具体文件说明。\n\n", 30)
				model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: answer, Streamed: true}})
				probe := newInlineRendererProbe(model.width)
				probe.draw(t, model.View())
				painted := ansi.Strip(strings.Join(model.printQueue, "\n") + probe.lastOutput)
				if !strings.Contains(painted, "Shell/Bash") || !strings.Contains(painted, "Python") {
					t.Fatalf("terminal did not paint the direct answer: %q", painted)
				}
				assertLiveFrameFits(t, model)
			})
		}
	}
}
