package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

const (
	pasteLineThreshold = 5
	pasteCharThreshold = 200
)

type pasteChip struct {
	Label string
	Text  string
}

type promptInput struct {
	area  textarea.Model
	chips []pasteChip
	next  int
}

func newPromptInput(noColor ...bool) promptInput {
	area := textarea.New()
	if len(noColor) > 0 && noColor[0] {
		styles := area.Styles()
		styles.Focused = textarea.StyleState{}
		styles.Blurred = textarea.StyleState{}
		styles.Cursor = textarea.CursorStyle{}
		area.SetStyles(styles)
	}
	area.Placeholder = "输入消息，/ 查看命令"
	area.Prompt = "❯ "
	area.CharLimit = 0
	area.SetHeight(1)
	area.ShowLineNumbers = false
	area.SetVirtualCursor(false)
	area.Focus()
	return promptInput{area: area}
}

func (input *promptInput) update(msg tea.Msg) tea.Cmd {
	if paste, ok := msg.(tea.PasteMsg); ok {
		text := paste.Content
		if shouldFoldPaste(text) {
			input.next++
			label := fmt.Sprintf("[粘贴 %d 行 · #%d]", strings.Count(text, "\n")+1, input.next)
			input.chips = append(input.chips, pasteChip{Label: label, Text: text})
			input.area.InsertString(label)
			return nil
		}
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && input.handleChipKey(key.String()) {
		return nil
	}
	var command tea.Cmd
	input.area, command = input.area.Update(msg)
	input.removeOrphanChips()
	return command
}

func (input *promptInput) handleChipKey(key string) bool {
	if len(input.chips) == 0 || (key != "left" && key != "right" && key != "backspace" && key != "delete") {
		return false
	}
	valueRunes := []rune(input.area.Value())
	cursor := input.cursorOffset()
	for _, chip := range input.chips {
		labelRunes := []rune(chip.Label)
		searchFrom := 0
		for searchFrom <= len(valueRunes)-len(labelRunes) {
			relative := runeSliceIndex(valueRunes[searchFrom:], labelRunes)
			if relative < 0 {
				break
			}
			start := searchFrom + relative
			end := start + len(labelRunes)
			switch key {
			case "left":
				if cursor > start && cursor <= end {
					input.setCursorOffset(start)
					return true
				}
			case "right":
				if cursor >= start && cursor < end {
					input.setCursorOffset(end)
					return true
				}
			case "backspace":
				if cursor > start && cursor <= end {
					input.replaceRuneRange(valueRunes, start, end, start)
					return true
				}
			case "delete":
				if cursor >= start && cursor < end {
					input.replaceRuneRange(valueRunes, start, end, start)
					return true
				}
			}
			searchFrom = end
		}
	}
	return false
}

func (input *promptInput) cursorOffset() int {
	lines := strings.Split(input.area.Value(), "\n")
	row := min(input.area.Line(), len(lines)-1)
	offset := 0
	for index := 0; index < row; index++ {
		offset += len([]rune(lines[index])) + 1
	}
	info := input.area.LineInfo()
	return offset + info.StartColumn + info.ColumnOffset
}

func (input *promptInput) setCursorOffset(offset int) {
	lines := strings.Split(input.area.Value(), "\n")
	row, column, remaining := 0, 0, max(0, offset)
	for index, line := range lines {
		length := len([]rune(line))
		if remaining <= length {
			row, column = index, remaining
			break
		}
		remaining -= length + 1
		row, column = index, length
	}
	input.area.CursorEnd()
	for input.area.Line() > row {
		input.area.CursorUp()
	}
	input.area.SetCursorColumn(column)
}

func (input *promptInput) replaceRuneRange(value []rune, start, end, cursor int) {
	updated := append(append([]rune(nil), value[:start]...), value[end:]...)
	input.area.SetValue(string(updated))
	input.setCursorOffset(cursor)
	input.removeOrphanChips()
}

func runeSliceIndex(value, target []rune) int {
	if len(target) == 0 || len(target) > len(value) {
		return -1
	}
	for index := 0; index <= len(value)-len(target); index++ {
		match := true
		for offset := range target {
			if value[index+offset] != target[offset] {
				match = false
				break
			}
		}
		if match {
			return index
		}
	}
	return -1
}

func shouldFoldPaste(value string) bool {
	return strings.Count(value, "\n")+1 >= pasteLineThreshold || utf8.RuneCountInString(value) >= pasteCharThreshold
}

func (input *promptInput) displayValue() string { return input.area.Value() }

func (input *promptInput) value() string {
	value := input.area.Value()
	for _, chip := range input.chips {
		value = strings.ReplaceAll(value, chip.Label, chip.Text)
	}
	return value
}

func (input *promptInput) setValue(value string) {
	input.chips = nil
	input.area.SetValue(value)
	input.area.CursorEnd()
}

func (input *promptInput) reset() {
	input.area.Reset()
	input.chips = nil
}

func (input *promptInput) resize(width, height int) {
	input.area.SetWidth(max(10, width))
	input.area.SetHeight(max(1, min(6, height)))
}

func (input *promptInput) removeOrphanChips() {
	value := input.area.Value()
	kept := input.chips[:0]
	for _, chip := range input.chips {
		if strings.Contains(value, chip.Label) {
			kept = append(kept, chip)
		}
	}
	input.chips = kept
}
