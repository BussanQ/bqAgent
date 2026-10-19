package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type toolInspection struct {
	selected   int
	listOffset int
	detail     bool
	offset     int
	cache      *toolDetailCache
}

type toolDetailCache struct {
	group    *toolViewCache
	selected int
	width    int
	lines    []string
}

func (model Model) toolHistoryReady() bool {
	return model.historyPrints == 0 && len(model.historyQueue) == 0 && len(model.printQueue) == 0
}

func (item toolDisplay) inspectorSummary(styles theme, width int) string {
	// A list row must not format output or todo bodies that will be discarded.
	if !item.hasResult {
		return ansi.Truncate(fmt.Sprintf("⚙ #%d %s %s", item.number, item.start.Name, summarizeArguments(item.start.Arguments, 180)), width, "…")
	}
	event := item.numbered(item.result)
	event.Preview = ""
	event.Arguments = nil
	line := toolSummary(event, styles, width, false)
	arguments := item.start.Arguments
	if len(arguments) == 0 {
		arguments = item.result.Arguments
	}
	if summary := summarizeArguments(arguments, 180); summary != "" {
		line += " · " + summary
	}
	return ansi.Truncate(line, width, "…")
}

func (model Model) toolInspectorHeader() []string {
	title := "本轮工具列表 · ← / Ctrl+O 返回"
	hint := "↑/↓ 选择 · Enter 或点击查看详情"
	if model.inspection.detail {
		title = "工具详情 · ← / 点击标题返回列表 · Ctrl+O 返回对话"
		hint = "↑/↓ / 滚轮 滚动 · PgUp/PgDn 翻页 · Home/End 首尾"
	}
	lines := []string{model.styles.accent.Render(title), model.styles.dim.Render(hint)}
	if model.quitConfirmationActive() {
		lines = append(lines, model.styles.dim.Render(quitConfirmHint))
	}
	// Keep a content row even in a short terminal; a header-only inspector
	// would accept scrolling without showing any change.
	return lines[:min(len(lines), max(1, model.frameLimit()-1))]
}

func (model Model) toolInspectorHeight() int {
	return max(0, model.frameLimit()-len(model.toolInspectorHeader()))
}

func (model *Model) normalizeToolInspection() {
	state := &model.inspection
	state.selected = min(max(0, state.selected), max(0, len(model.tools.items)-1))
	height := max(1, model.toolInspectorHeight())
	state.listOffset = min(max(0, state.listOffset), max(0, len(model.tools.items)-height))
	if state.selected < state.listOffset {
		state.listOffset = state.selected
	} else if state.selected >= state.listOffset+height {
		state.listOffset = state.selected - height + 1
	}
	if state.detail {
		state.offset = min(max(0, state.offset), max(0, len(model.toolDetailLines())-height))
	}
}

func (model *Model) handleToolInspectorKey(name string) []tea.Cmd {
	switch name {
	case "ctrl+o":
		model.toolInspector = false
	case "esc":
		model.backFromToolDetail()
	case "ctrl+q":
		model.toolInspector = false
		return model.requestQuit()
	case "enter", "right":
		if !model.inspection.detail && len(model.tools.items) > 0 {
			model.inspection.detail = true
			model.inspection.offset = 0
		}
	case "left":
		model.backFromToolDetail()
	case "up", "down", "pgup", "pgdown", "home", "end":
		step := 1
		if name == "pgup" || name == "pgdown" {
			step = max(1, model.toolInspectorHeight())
		}
		position := &model.inspection.selected
		end := max(0, len(model.tools.items)-1)
		if model.inspection.detail {
			position = &model.inspection.offset
			end = max(0, len(model.toolDetailLines())-max(1, model.toolInspectorHeight()))
		}
		switch name {
		case "up", "pgup":
			*position -= step
		case "down", "pgdown":
			*position += step
		case "home":
			*position = 0
		case "end":
			*position = end
		}
	}
	model.normalizeToolInspection()
	return nil
}

func (model *Model) backFromToolDetail() {
	if model.inspection.detail {
		model.inspection.detail = false
	} else {
		model.toolInspector = false
	}
}

func (model *Model) handleToolInspectorMouse(event tea.Mouse) {
	if event.Button != tea.MouseLeft || event.X < 0 || event.X >= model.contentWidth() || event.Y < 0 {
		return
	}
	if event.Y == 0 {
		model.backFromToolDetail()
		return
	}
	if model.inspection.detail {
		return
	}
	row := event.Y - len(model.toolInspectorHeader())
	index := model.inspection.listOffset + row
	if row >= 0 && row < model.toolInspectorHeight() && index < len(model.tools.items) {
		model.inspection.selected = index
		model.inspection.detail = true
		model.inspection.offset = 0
	}
}

func (model Model) toolDetailLines() []string {
	state := model.inspection
	if state.selected < 0 || state.selected >= len(model.tools.items) {
		return nil
	}
	width := model.contentWidth()
	cache := state.cache
	if cache != nil && cache.lines != nil && cache.group == model.tools.cache && cache.selected == state.selected && cache.width == width {
		return cache.lines
	}
	rendered := model.tools.items[state.selected].detailLines(width)
	if cache != nil {
		cache.group, cache.selected, cache.width, cache.lines = model.tools.cache, state.selected, width, rendered
	}
	return rendered
}

func (item toolDisplay) detailLines(width int) []string {
	event := item.start
	status := "运行中"
	if item.hasResult {
		event = item.result
		status = "成功"
		if strings.EqualFold(event.Status, "failed") || strings.EqualFold(event.Status, "error") {
			status = "失败"
		}
	}
	name := item.start.Name
	if name == "" {
		name = event.Name
	}
	lines := []string{fmt.Sprintf("#%d %s · %s", item.number, name, status)}
	if event.DurationMS > 0 {
		lines = append(lines, fmt.Sprintf("耗时：%dms", event.DurationMS))
	}
	if item.start.ID != "" {
		lines = append(lines, "调用 ID："+item.start.ID)
	}
	arguments := item.start.Arguments
	if len(arguments) == 0 {
		arguments = event.Arguments
	}
	if len(arguments) > 0 {
		data, err := json.MarshalIndent(arguments, "", "  ")
		if err == nil {
			lines = append(lines, "", "参数", string(data))
		} else {
			lines = append(lines, "", "参数", fmt.Sprint(arguments))
		}
	}
	lines = append(lines, "", "执行输出")
	if event.Truncated {
		lines = append(lines, "输出已截断：工具仅提供了部分结果")
	}
	output := plainToolOutput(event.Preview)
	if output == "" {
		output = "（无输出）"
		if !item.hasResult {
			output = "（等待工具完成）"
		}
	}
	lines = append(lines, output)
	return strings.Split(ansi.Hardwrap(plainToolOutput(strings.Join(lines, "\n")), width, true), "\n")
}

// Completed replies may already live partly in native scrollback. Inspect on
// the alternate screen so opening details never splits or relocates that reply.
func (model Model) toolInspectorView() tea.View {
	model.normalizeToolInspection()
	width, height := model.contentWidth(), model.toolInspectorHeight()
	lines := model.toolInspectorHeader()
	if model.inspection.detail {
		detail := model.toolDetailLines()
		offset := min(model.inspection.offset, len(detail))
		lines = append(lines, detail[offset:min(len(detail), offset+height)]...)
	} else {
		start := model.inspection.listOffset
		for i := start; i < min(len(model.tools.items), start+height); i++ {
			item := model.tools.items[i]
			summary := item.inspectorSummary(model.styles, max(1, width-2))
			prefix := "  "
			if i == model.inspection.selected {
				prefix = "› "
			}
			lines = append(lines, prefix+summary)
		}
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.ReportFocus = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}
