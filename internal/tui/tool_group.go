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
}

type toolGroup struct {
	items     []toolDisplay
	committed bool // History owns the record; the live drawer is only a detail inspector.
	expanded  bool
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
			group.items = append(group.items, toolDisplay{start: event})
		}
	} else {
		index := group.find(event)
		if index < 0 {
			group.items = append(group.items, toolDisplay{start: ToolEvent{Kind: "tool_start", Seq: event.Seq, ID: event.ID, Name: event.Name}, result: event, hasResult: true})
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
	if cache != nil && cache.lines != nil && cache.width == width && cache.expanded == group.expanded {
		return cache.lines
	}
	rendered := strings.Split(group.render(styles, true, width), "\n")
	// Keep the clickable summary on one physical terminal row.
	lines := []string{ansi.Truncate(rendered[0], width, "…")}
	if len(rendered) > 1 {
		lines = append(lines, strings.Split(ansi.Hardwrap(strings.Join(rendered[1:], "\n"), width, false), "\n")...)
	}
	if cache != nil {
		cache.width, cache.expanded, cache.lines = width, group.expanded, lines
	}
	return lines
}

func (group toolGroup) viewport(styles theme, width, height int) []string {
	if height <= 0 {
		return nil
	}
	lines := group.viewLines(styles, width)
	if len(lines) <= height {
		return lines
	}
	if height == 1 {
		return lines[:1]
	}
	if height == 2 {
		return []string{lines[0], styles.dim.Render(ansi.Truncate("增大窗口查看详情", width, "…"))}
	}
	// Reserve the summary and paging hint; scrolling never prints to history.
	count := max(0, height-2)
	start := min(group.offset, max(0, len(lines)-1-count))
	visible := []string{lines[0]}
	visible = append(visible, lines[1+start:1+start+count]...)
	visible = append(visible, styles.dim.Render(ansi.Truncate(fmt.Sprintf("详情 %d–%d/%d · PgUp/PgDn 翻页", start+1, start+count, len(lines)-1), width, "…")))
	return visible
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
			hint = "收起详情"
		} else if interactive {
			hint = "点击或 Ctrl+O 展开"
		}
		header = fmt.Sprintf("%s %s · %s", marker, header, hint)
	} else {
		header = "◆ " + header
		if interactive && group.canExpand() {
			if group.expanded {
				header += " · 点击或 Ctrl+O 收起详情"
			} else {
				header += " · 点击或 Ctrl+O 展开详情"
			}
		}
	}
	lines := []string{styles.accent.Render(header)}
	if !collapsible || group.expanded {
		for _, item := range group.items {
			lines = append(lines, indentToolItem(item.render(styles, max(1, width-2), interactive && group.expanded)))
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
		return toolSummary(item.start, styles, width, expanded)
	}
	result := item.result
	result.Seq = item.start.Seq
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
