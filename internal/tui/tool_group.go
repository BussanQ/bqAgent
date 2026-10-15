package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const toolCollapseThreshold = 5

type toolDisplay struct {
	start     ToolEvent
	result    ToolEvent
	hasResult bool
	// number is the call's position in this turn. Event Seq counts starts and
	// results together, so it skips numbers and keeps growing across turns.
	number int
}

func (item toolDisplay) numbered(event ToolEvent) ToolEvent {
	event.Seq = uint64(item.number)
	return event
}

type toolGroup struct {
	items     []toolDisplay
	committed bool // History owns the record; the live drawer is only a detail inspector.
	expanded  bool
	detail    int // One-based tool index; zero shows the summary list.
	offset    int
	cache     *toolViewCache
}

func (group toolGroup) liveVisible() bool {
	return len(group.items) > 0 && (!group.committed || group.expanded)
}

func (group toolGroup) canExpand() bool {
	if len(group.items) > toolCollapseThreshold || group.committed {
		return true
	}
	for _, item := range group.items {
		if strings.TrimSpace(item.result.Preview) != "" {
			return true
		}
	}
	return false
}

type toolViewCache struct {
	width    int
	expanded bool
	detail   int
	lines    []string
}

func (group *toolGroup) add(event ToolEvent) (becameExpandable bool) {
	previous := group.canExpand()
	group.cache = &toolViewCache{}
	if event.Kind == "tool_start" {
		// Replayed starts and results update the same call, including a start
		// arriving after its result. Distinct IDs remain distinct activities.
		index := -1
		if event.ID != "" {
			index = group.find(event)
		}
		if index >= 0 {
			group.items[index].start = event
		} else {
			group.items = append(group.items, toolDisplay{start: event, number: len(group.items) + 1})
		}
	} else {
		index := group.find(event)
		if index < 0 {
			group.items = append(group.items, toolDisplay{start: ToolEvent{Kind: "tool_start", Seq: event.Seq, ID: event.ID, Name: event.Name}, result: event, hasResult: true, number: len(group.items) + 1})
		} else {
			group.items[index].result = event
			group.items[index].hasResult = true
		}
	}
	return !previous && group.canExpand()
}

// Cache formatting until a tool changes, rather than formatting the whole
// drawer again for every spinner tick, token and input event.
func (group toolGroup) viewLines(styles theme, width int) []string {
	if len(group.items) == 0 {
		return nil
	}
	cache := group.cache
	if cache != nil && cache.lines != nil && cache.width == width && cache.expanded == group.expanded && cache.detail == group.detail {
		return cache.lines
	}
	rendered := strings.Split(group.render(styles, true, width), "\n")
	// Keep the clickable summary on one physical terminal row.
	lines := []string{ansi.Truncate(rendered[0], width, "…")}
	if len(rendered) > 1 {
		lines = append(lines, strings.Split(ansi.Hardwrap(strings.Join(rendered[1:], "\n"), width, false), "\n")...)
	}
	if cache != nil {
		cache.width, cache.expanded, cache.detail, cache.lines = width, group.expanded, group.detail, lines
	}
	return lines
}

func (group toolGroup) viewport(styles theme, width, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := group.viewLines(styles, width)
	if !group.expanded || len(lines) <= 1 || height == 1 {
		return lines[:min(len(lines), height)]
	}
	bodyHeight := height - 1
	offset := min(max(0, group.offset), max(0, len(lines)-1-bodyHeight))
	visible := []string{lines[0]}
	return append(visible, lines[1+offset:min(len(lines), 1+offset+bodyHeight)]...)
}

func (group toolGroup) find(event ToolEvent) int {
	for index := len(group.items) - 1; index >= 0; index-- {
		item := group.items[index]
		if event.ID != "" && item.start.ID == event.ID {
			return index
		}
		if event.ID == "" && !item.hasResult && item.start.Name == event.Name {
			return index
		}
	}
	return -1
}

func (group toolGroup) render(styles theme, interactive bool, width int) string {
	if len(group.items) == 0 {
		return ""
	}
	running, succeeded, failed := group.counts()
	details := make([]string, 0, 3)
	if succeeded > 0 {
		details = append(details, fmt.Sprintf("%d 成功", succeeded))
	}
	if failed > 0 {
		details = append(details, fmt.Sprintf("%d 失败", failed))
	}
	if running > 0 {
		details = append(details, fmt.Sprintf("%d 运行中", running))
	}
	header := fmt.Sprintf("工具活动 · %d 次", len(group.items))
	if len(details) > 0 {
		header += " · " + strings.Join(details, " · ")
	}
	collapsible := len(group.items) > toolCollapseThreshold
	if collapsible {
		marker := "▶"
		hint := "详情已合并"
		if group.expanded {
			marker = "▼"
			hint = "点击工具查看详情 · Ctrl+O 收起"
		} else if interactive {
			hint = "点击或 Ctrl+O 展开"
		}
		header = fmt.Sprintf("%s %s · %s", marker, header, hint)
	} else {
		header = "◆ " + header
		if interactive && group.canExpand() {
			if group.expanded {
				header += " · 点击工具查看详情 · Ctrl+O 收起"
			} else {
				header += " · 点击或 Ctrl+O 展开详情"
			}
		}
	}
	if interactive && group.expanded && group.detail > 0 && group.detail <= len(group.items) {
		lines := []string{styles.accent.Render("工具详情 · 点击标题返回列表 · 滚轮滚动 · Ctrl+O 收起")}
		return strings.Join(append(lines, group.items[group.detail-1].detailLines(width)...), "\n")
	}
	lines := []string{styles.accent.Render(header)}
	if !collapsible || group.expanded {
		for _, item := range group.items {
			if interactive && group.expanded {
				lines = append(lines, indentToolItem(item.inspectorSummary(styles, max(1, width-2))))
			} else {
				lines = append(lines, indentToolItem(item.render(styles, max(1, width-2), false)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (group toolGroup) counts() (running, succeeded, failed int) {
	for _, item := range group.items {
		if !item.hasResult {
			running++
			continue
		}
		status := strings.ToLower(strings.TrimSpace(item.result.Status))
		if status == "failed" || status == "error" {
			failed++
		} else {
			succeeded++
		}
	}
	return running, succeeded, failed
}

func (item toolDisplay) render(styles theme, width int, expanded bool) string {
	if !item.hasResult {
		return toolSummary(item.numbered(item.start), styles, width, expanded)
	}
	result := item.numbered(item.result)
	if len(result.Arguments) == 0 {
		result.Arguments = item.start.Arguments
	}
	rendered := toolSummary(result, styles, width, expanded)
	if result.Name != "todo_write" {
		if arguments := summarizeArguments(item.start.Arguments, 180); arguments != "" {
			parts := strings.SplitN(rendered, "\n", 2)
			parts[0] += " · " + arguments
			rendered = strings.Join(parts, "\n")
		}
	}
	// Long arguments must not turn the one-row tool heading into a screenful.
	parts := strings.SplitN(rendered, "\n", 2)
	parts[0] = ansi.Truncate(parts[0], width, "…")
	rendered = strings.Join(parts, "\n")
	return rendered
}

func indentToolItem(value string) string {
	return "  " + strings.ReplaceAll(value, "\n", "\n  ")
}
