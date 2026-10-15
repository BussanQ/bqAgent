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
