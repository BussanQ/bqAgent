package tui

import (
	"fmt"
	"strings"
)

const toolCollapseThreshold = 5

type toolDisplay struct {
	start     ToolEvent
	result    ToolEvent
	hasResult bool
}

type toolGroup struct {
	items    []toolDisplay
	expanded bool
}

func (group *toolGroup) add(event ToolEvent) (crossedThreshold bool) {
	previous := len(group.items)
	if event.Kind == "tool_start" {
		group.items = append(group.items, toolDisplay{start: event})
	} else {
		index := group.find(event)
		if index < 0 {
			group.items = append(group.items, toolDisplay{start: ToolEvent{Kind: "tool_start", Seq: event.Seq, ID: event.ID, Name: event.Name}, result: event, hasResult: true})
		} else {
			group.items[index].result = event
			group.items[index].hasResult = true
		}
	}
	return previous <= toolCollapseThreshold && len(group.items) > toolCollapseThreshold
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

func (group toolGroup) render(styles theme, interactive bool) string {
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
			hint = "点击或 Ctrl+T 展开"
		}
		header = fmt.Sprintf("%s %s · %s", marker, header, hint)
	} else {
		header = "◆ " + header
	}
	lines := []string{styles.accent.Render(header)}
	if !collapsible || group.expanded {
		for _, item := range group.items {
			lines = append(lines, indentToolItem(item.render(styles)))
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

func (item toolDisplay) render(styles theme) string {
	if !item.hasResult {
		return toolSummary(item.start, styles)
	}
	result := item.result
	result.Seq = item.start.Seq
	if len(result.Arguments) == 0 {
		result.Arguments = item.start.Arguments
	}
	rendered := toolSummary(result, styles)
	if result.Name != "todo_write" {
		if arguments := summarizeArguments(item.start.Arguments, 180); arguments != "" {
			parts := strings.SplitN(rendered, "\n", 2)
			parts[0] += " · " + arguments
			rendered = strings.Join(parts, "\n")
		}
	}
	return rendered
}

func indentToolItem(value string) string {
	return "  " + strings.ReplaceAll(value, "\n", "\n  ")
}
