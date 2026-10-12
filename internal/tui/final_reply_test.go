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

func TestFinalReplyPagingKeepsWholeConclusion(t *testing.T) {
	model := toolDrawerModel(t, 10)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	var lines []string
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf("结论第%03d行", i))
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: strings.Join(lines, "\n\n")}})
	if !strings.Contains(model.View().Content, lines[0]) {
		t.Fatal("long conclusion did not start at the first line")
	}
	for i := 0; i < 30 && !strings.Contains(model.View().Content, lines[59]); i++ {
		model.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		assertLiveFrameFits(t, model)
	}
	if !strings.Contains(model.View().Content, lines[59]) {
		t.Fatal("last conclusion line is unreachable")
	}
	for i := 0; i < 30; i++ {
		model.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	if !strings.Contains(model.View().Content, lines[0]) {
		t.Fatal("first conclusion line is unreachable")
	}
	for _, size := range [][2]int{{18, 9}, {72, 24}} {
		updated, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		model = updated.(Model)
		assertLiveFrameFits(t, model)
	}
	model.commitTurn()
	printed := strings.Join(model.printQueue, "\n")
	for _, line := range lines {
		if strings.Count(printed, line) != 1 {
			t.Fatalf("paging lost or duplicated %q in committed history", line)
		}
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
				painted := ansi.Strip(probe.lastOutput)
				if !strings.Contains(painted, "Shell/Bash") || !strings.Contains(painted, "Python") {
					t.Fatalf("terminal did not paint the direct answer: %q", painted)
				}
				assertLiveFrameFits(t, model)
			})
		}
	}
}
