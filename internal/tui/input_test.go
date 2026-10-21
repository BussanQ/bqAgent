package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPromptNewlineKeepsFirstLineAndCursor(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.input = newPromptInput(true)
	model.resizeInput()
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "第一行"})
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "第二行"})
	rows := strings.Split(ansi.Strip(model.input.area.View()), "\n")
	if len(rows) != 2 || strings.TrimSpace(rows[0]) != "❯ 第一行" || strings.TrimSpace(rows[1]) != "第二行" {
		t.Fatalf("multiline input rows = %q", rows)
	}
	cursor := model.input.area.Cursor()
	if cursor == nil || cursor.Y != 1 || cursor.X != 8 {
		t.Fatalf("cursor = %#v, want (8, 1)", cursor)
	}
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if cursor := model.input.area.Cursor(); cursor == nil || cursor.Y != 0 {
		t.Fatalf("cursor after up = %#v, want row 0", cursor)
	}
}

func TestPromptDynamicHeightAndFirstLinePrompt(t *testing.T) {
	input := newPromptInput(true)
	input.resize(12, 6)
	input.setValue("abcdefghijklmnop")
	rows := strings.Split(ansi.Strip(input.area.View()), "\n")
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "❯ ") || !strings.HasPrefix(rows[1], "  ") || strings.Count(strings.Join(rows, "\n"), "❯") != 1 {
		t.Fatalf("wrapped input rows = %q", rows)
	}
	input.reset()
	for i, line := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		if i > 0 {
			input.update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}
		input.update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: line})
		input.resize(12, 6)
	}
	if input.area.Height() != 6 || input.area.Cursor().Y != 5 {
		t.Fatalf("capped height=%d cursor=%#v", input.area.Height(), input.area.Cursor())
	}
	input.resize(12, 3)
	if input.area.Height() != 3 || input.area.Cursor().Y != 2 {
		t.Fatalf("small terminal height=%d cursor=%#v", input.area.Height(), input.area.Cursor())
	}
	input.resize(12, 6)
	input.setValue("first\nsecond")
	rows = strings.Split(ansi.Strip(input.area.View()), "\n")
	if len(rows) != 2 || strings.TrimSpace(rows[0]) != "❯ first" || input.area.Cursor().Y != 1 {
		t.Fatalf("shrunk input rows=%q cursor=%#v", rows, input.area.Cursor())
	}
	input.reset()
	if input.area.Height() != 1 || input.area.Cursor().Y != 0 {
		t.Fatalf("reset height=%d cursor=%#v", input.area.Height(), input.area.Cursor())
	}
}
