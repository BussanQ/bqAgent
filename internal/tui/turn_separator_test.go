package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTurnSeparatorClearlySeparatesQuestions(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.enqueuePrint("上一轮回答末尾")
	model.enqueueUserTurn("最新问题\n补充说明")
	model.enqueuePrint("最新回答")
	text := ansi.Strip(strings.Join(model.printQueue, "\n"))
	if !strings.Contains(text, "上一轮回答末尾\n\n\n──") {
		t.Fatalf("new turn needs visible space and a distinct rule: %q", text)
	}
	if strings.Count(text, "❯❯") != 1 || !strings.Contains(text, "\n\n▸ 最新问题\n  补充说明\n\n最新回答") {
		t.Fatalf("turn heading or question spacing missing: %q", text)
	}
}

func TestTurnSeparatorFitsNarrowTerminals(t *testing.T) {
	model := toolDrawerModel(t, 0)
	for _, width := range []int{8, 18, 32, 72, 120} {
		model.width = width
		separator := ansi.Strip(model.renderTurnSeparator())
		if strings.Trim(separator, "\n ─❯") != "" {
			t.Fatalf("separator must contain only rule and prompt symbols: %q", separator)
		}
		for _, line := range strings.Split(separator, "\n") {
			if ansi.StringWidth(line) > model.contentWidth() {
				t.Fatalf("separator wraps at width %d: %q", width, line)
			}
		}
		if width >= 32 && !strings.Contains(separator, "❯❯") {
			t.Fatalf("turn heading missing at width %d", width)
		}
	}
}

func TestRestoredHistoryUsesClearTurnSeparators(t *testing.T) {
	model := toolDrawerModel(t, 0)
	text := ansi.Strip(strings.Join(model.renderHistory(History{ID: "session", Messages: []HistoryMessage{
		{Role: "user", Content: "第一个问题"},
		{Role: "assistant", Content: "第一个回答"},
		{Role: "user", Content: "第二个问题"},
		{Role: "assistant", Content: "第二个回答"},
	}}), "\n"))
	if strings.Count(text, "❯❯") != 2 || strings.Count(text, "\n\n\n──") != 2 {
		t.Fatalf("history must separate each user turn consistently: %q", text)
	}
	if strings.Index(text, "第一个回答") > strings.Index(text, "第二个问题") {
		t.Fatal("turn separators reordered conversation history")
	}
}
