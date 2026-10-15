package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatFrameKeepsInputAtBottom(t *testing.T) {
	model := toolDrawerModel(t, 0)
	for _, size := range [][2]int{{72, 24}, {100, 40}, {18, 9}, {8, 5}, {72, 24}} {
		updated, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		model = updated.(Model)
		for _, text := range []string{"", "short reply", strings.Repeat("stream line\n", 80)} {
			model.stream = text
			view := model.View()
			if got := strings.Count(view.Content, "\n") + 1; got != model.frameLimit() {
				t.Fatalf("size=%v stream=%d: frame has %d rows, want %d", size, len(text), got, model.frameLimit())
			}
			assertLiveFrameFits(t, model)
			cursor := model.input.area.Cursor()
			if cursor != nil && view.Cursor != nil {
				want := model.frameLimit() - len(model.footerLines()) + 1 + cursor.Y
				if view.Cursor.Y != want {
					t.Fatalf("input cursor row=%d, want bottom-anchored row=%d", view.Cursor.Y, want)
				}
			}
		}
	}
}

func TestInlineStreamingDoesNotResizeOrClearFrame(t *testing.T) {
	base := toolDrawerModel(t, 0)
	base.sequence = 1
	base.active = &activeTurn{sequence: 1, cancel: func() {}}
	var model tea.Model = newInlineModel(base)
	probe := newInlineRendererProbe(base.width)
	probe.draw(t, model.View())
	for i := 0; i < 80; i++ {
		model, _ = model.Update(turnEvent{sequence: 1, kind: "token", text: fmt.Sprintf("stream-%03d\n", i)})
		if model.(inlineModel).pending {
			t.Fatal("streaming scheduled a clear/repaint")
		}
		if got := strings.Count(model.View().Content, "\n") + 1; got != base.frameLimit() {
			t.Fatalf("streaming changed frame height to %d", got)
		}
		probe.draw(t, model.View())
		if strings.Contains(probe.lastOutput, "\x1b[J") || strings.Contains(probe.lastOutput, "\x1b[2J") {
			t.Fatal("streaming erased the screen")
		}
	}
}

func TestTranscriptResizeAndClearPreserveOnlyUnprintedRows(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.enqueuePrint("old-row\n" + strings.Repeat("宽", 28) + "\nrecent-row")
	updated, _ := model.Update(nil)
	model = updated.(Model)
	before := strings.Join(model.transcript, "\n")
	model.width, model.height = 18, 7
	model.resizeInput()
	printed := model.flushTranscript()
	if printed == "" {
		t.Fatal("narrow resize must move overflowing retained rows to history")
	}
	assertLiveFrameFits(t, model)
	combined := printed + "\n" + strings.Join(model.transcript, "\n")
	if strings.ReplaceAll(combined, "\n", "") != strings.ReplaceAll(before, "\n", "") {
		t.Fatal("resize lost or duplicated retained transcript rows")
	}
	if model.flushTranscript() != "" {
		t.Fatal("unchanged layout reprinted transcript rows")
	}
	model.width, model.height = 72, 24
	model.resizeInput()
	if model.flushTranscript() != "" || strings.Contains(model.View().Content, "old-row") {
		t.Fatal("growing the terminal replayed previously printed history")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	model = updated.(Model)
	if len(model.transcript) != 0 || strings.Contains(model.View().Content, "recent-row") {
		t.Fatal("clearing the viewport left retained transcript visible")
	}
	model.enqueuePrint("another-row")
	model.flushTranscript()
	model.clearSession()
	if len(model.transcript) != 0 {
		t.Fatal("clearing the session retained old conversation rows")
	}
}

func TestStartupArtCentersInUnusedSpace(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.width, model.height = 100, 60
	model.resizeInput()
	updated, _ := model.Update(startupMsg{})
	model = updated.(Model)
	rows := strings.Split(model.View().Content, "\n")
	first, last := -1, -1
	for i, row := range rows {
		if strings.TrimSpace(row) == ".---." {
			first = i
		}
		if strings.TrimSpace(row) == "bqAgent" {
			last = i
		}
	}
	if first < 0 || last < first {
		t.Fatal("startup ASCII robot missing")
	}
	spaceEnd := len(rows) - len(model.liveLines())
	above, below := first-len(model.transcript), spaceEnd-last-1
	if above < 1 || below < above || below-above > 1 {
		t.Fatalf("art is not vertically centered: above=%d below=%d", above, below)
	}
	label := rows[last]
	left := len(label) - len(strings.TrimLeft(label, " "))
	right := model.contentWidth() - len(label)
	if left-right < -1 || left-right > 1 {
		t.Fatalf("art is not horizontally centered: left=%d right=%d", left, right)
	}
	if !strings.Contains(rows[0], "bqAgent") || len(rows) != model.frameLimit() {
		t.Fatal("startup art moved the banner or changed frame height")
	}
	if strings.Contains(strings.Join(model.transcript, "\n"), ".---.") {
		t.Fatal("decorative art must not enter conversation history")
	}
	assertLiveFrameFits(t, model)
	model.input.setValue("first question")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	defer model.active.cancel()
	if strings.Contains(model.View().Content, ".---.") {
		t.Fatal("startup art must disappear after submitting a question")
	}
}

func TestStartupArtHidesWhenUnavailable(t *testing.T) {
	for _, state := range []string{"short", "narrow", "resumed", "completed"} {
		t.Run(state, func(t *testing.T) {
			model := toolDrawerModel(t, 0)
			model.width, model.height = 100, 60
			switch state {
			case "short":
				model.height = 12
			case "narrow":
				model.width = 20
			case "resumed":
				model.sessionID = "existing-session"
			case "completed":
				model.sequence = 1
			}
			model.resizeInput()
			updated, _ := model.Update(startupMsg{})
			model = updated.(Model)
			if strings.Contains(model.View().Content, ".---.") {
				t.Fatal("startup art should be hidden")
			}
			assertLiveFrameFits(t, model)
		})
	}
}

func TestStartupContentStaysAtTopThroughFirstResponse(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.height = 60
	model.resizeInput()
	updated, _ := model.Update(startupMsg{})
	model = updated.(Model)
	startup := strings.Split(model.View().Content, "\n")[0]
	if !strings.Contains(startup, "bqAgent") {
		t.Fatal("startup banner should be at the top before the first question")
	}
	for _, stage := range []string{"input", "submit", "token", "tool"} {
		t.Run(stage, func(t *testing.T) {
			var message tea.Msg
			switch stage {
			case "input":
				message = tea.PasteMsg{Content: "first question"}
			case "submit":
				message = tea.KeyPressMsg{Code: tea.KeyEnter}
			case "token":
				message = turnEvent{sequence: model.sequence, kind: "token", text: "short response"}
			case "tool":
				message = turnEvent{sequence: model.sequence, kind: "tool", tool: ToolEvent{Kind: "tool_start", ID: "first-tool", Name: "read_file"}}
			}
			updated, _ := model.Update(message)
			model = updated.(Model)
			view := model.View()
			rows := strings.Split(view.Content, "\n")
			if rows[0] != startup {
				t.Fatal("startup content moved when the conversation advanced")
			}
			for i, row := range model.transcript {
				if rows[i] != row {
					t.Fatal("startup content and question should remain at the top")
				}
			}
			if len(rows) != model.frameLimit() || view.Cursor == nil || view.Cursor.Y != len(rows)-len(model.footerLines())+1+model.input.area.Cursor().Y {
				t.Fatal("startup layout changed frame height or bottom input position")
			}
		})
	}
	if model.active != nil {
		model.active.cancel()
	}
}

func TestChatWithToolsKeepsActivityAtTop(t *testing.T) {
	for _, count := range []int{1, 6} {
		for _, expanded := range []bool{false, true} {
			for _, state := range []string{"tools_only", "streaming", "completed"} {
				t.Run(fmt.Sprintf("count_%d/expanded_%t/%s", count, expanded, state), func(t *testing.T) {
					model := toolDrawerModel(t, count)
					model.height = 60
					model.resizeInput()
					model.tools.expanded = expanded
					model.enqueuePrint("recent-question")
					switch state {
					case "streaming":
						model.stream = "short response"
					case "completed":
						model.reply = "short response"
					}
					updated, command := model.Update(nil)
					model = updated.(Model)
					if command != nil {
						t.Fatal("short conversation should remain in the frame")
					}
					view := model.View()
					rows := strings.Split(view.Content, "\n")
					if rows[0] != "recent-question" || !strings.Contains(rows[1], "工具活动") {
						t.Fatalf("tools should follow the question at the top:\n%s", view.Content)
					}
					footerStart := len(rows) - len(model.footerLines())
					gapEnd := footerStart
					if state != "tools_only" {
						gapEnd--
						if !strings.Contains(rows[gapEnd], "short response") {
							t.Fatal("response should remain adjacent to the input footer")
						}
					}
					toolEnd := 1 + len(model.tools.viewport(model.styles, model.contentWidth(), model.toolViewportHeight()))
					if toolEnd >= gapEnd {
						t.Fatal("tall terminal should have spare rows below tools")
					}
					for _, row := range rows[toolEnd:gapEnd] {
						if strings.TrimSpace(row) != "" {
							t.Fatalf("expected unused row below tools: %q", row)
						}
					}
					if len(rows) != model.frameLimit() || view.Cursor == nil || view.Cursor.Y != footerStart+1+model.input.area.Cursor().Y {
						t.Fatal("tool positioning changed frame height or input cursor")
					}
					assertLiveFrameFits(t, model)
				})
			}
		}
	}
}

func TestTranscriptPaddingKeepsToolSummaryClickable(t *testing.T) {
	model := toolDrawerModel(t, 6)
	model.enqueuePrint("recent-question")
	updated, _ := model.Update(nil)
	model = updated.(Model)
	rows := strings.Split(model.View().Content, "\n")
	for row, text := range rows {
		if !strings.Contains(text, "工具活动") {
			continue
		}
		updated, command := model.Update(tea.MouseClickMsg{X: 1, Y: model.height - len(rows) + row, Button: tea.MouseLeft})
		model = updated.(Model)
		if command != nil || !model.tools.expanded || !strings.Contains(model.View().Content, "recent-question") {
			t.Fatal("retained transcript or padding broke the tool summary click")
		}
		return
	}
	t.Fatal("tool summary missing from frame")
}

func TestChatWithoutToolsKeepsTranscriptAdjacentToLiveContent(t *testing.T) {
	for _, state := range []string{"idle", "streaming", "completed"} {
		t.Run(state, func(t *testing.T) {
			model := toolDrawerModel(t, 0)
			model.sequence = 1
			model.enqueuePrint("recent-question\nrecent-answer")
			switch state {
			case "streaming":
				model.stream = "short response"
			case "completed":
				model.reply = "short response"
			}
			updated, command := model.Update(nil)
			model = updated.(Model)
			if command != nil {
				t.Fatal("short transcript should remain in the frame")
			}
			rows := strings.Split(model.View().Content, "\n")
			body := model.liveLines()
			start := len(rows) - len(body) - 2
			if start < 0 || rows[start] != "recent-question" || rows[start+1] != "recent-answer" {
				t.Fatalf("retained transcript should immediately precede live content:\n%s", model.View().Content)
			}
			for _, row := range rows[:start] {
				if strings.TrimSpace(row) != "" {
					t.Fatalf("unused frame rows should be above the transcript: %q", row)
				}
			}
			if len(rows) != model.frameLimit() {
				t.Fatalf("frame height = %d, want %d", len(rows), model.frameLimit())
			}
		})
	}
}

func TestChatKeepsRecentTranscriptOnScreen(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.enqueuePrint("recent-question")
	updated, command := model.Update(nil)
	model = updated.(Model)
	if command != nil || !strings.Contains(model.View().Content, "recent-question") {
		t.Fatal("short transcript must stay above the input instead of immediately scrolling off screen")
	}
	model.stream = "short response"
	updated, command = model.Update(nil)
	model = updated.(Model)
	if command != nil || !strings.Contains(model.View().Content, "recent-question") || !strings.Contains(model.View().Content, "short response") {
		t.Fatal("streaming hid the recent question")
	}
}
