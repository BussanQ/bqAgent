package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
)

const accentColor = "#A970FF"

type theme struct {
	accent lipgloss.Style
	dim    lipgloss.Style
	error  lipgloss.Style
	ok     lipgloss.Style
	user   lipgloss.Style
}

func newTheme(noColor bool) theme {
	if noColor {
		return theme{}
	}
	return theme{
		accent: lipgloss.NewStyle().Foreground(lipgloss.Color(accentColor)).Bold(true),
		dim:    lipgloss.NewStyle().Foreground(lipgloss.Color("#777777")),
		error:  lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")),
		ok:     lipgloss.NewStyle().Foreground(lipgloss.Color("#69DB7C")),
		user:   lipgloss.NewStyle().Foreground(lipgloss.Color("#C9A7FF")).Bold(true),
	}
}

func renderMarkdown(value string, width int, noColor bool) string {
	style := "dark"
	if noColor {
		style = "notty"
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(max(10, width-2)),
	)
	if err != nil {
		return strings.TrimSpace(value)
	}
	rendered, err := renderer.Render(strings.TrimSpace(value))
	if err != nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(rendered)
}

func renderStreaming(value string, width int) string {
	return strings.TrimRight(ansi.Hardwrap(value, max(10, width-2), false), "\n")
}

func toolSummary(event ToolEvent, styles theme) string {
	sequence := event.Seq
	if event.Kind == "tool_start" {
		line := fmt.Sprintf("%s %s %s", styles.accent.Render(fmt.Sprintf("⚙ #%d", sequence)), event.Name, summarizeArguments(event.Arguments, 180))
		if event.Name == "todo_write" {
			if block := todoProgress(event.Arguments); block != "" {
				line += "\n" + block
			}
		}
		return line
	}
	status := strings.ToLower(strings.TrimSpace(event.Status))
	failed := status == "error" || status == "failed"
	mark := styles.ok.Render("✓")
	if failed {
		mark = styles.error.Render("✗")
	}
	line := fmt.Sprintf("%s #%d %s", mark, sequence, event.Name)
	if event.DurationMS > 0 {
		line += fmt.Sprintf(" · %s", time.Duration(event.DurationMS)*time.Millisecond)
	}
	if event.Truncated {
		line += " · 输出已截断"
	}
	if failed && strings.TrimSpace(event.Preview) != "" {
		line += "\n" + styles.error.Render(indentPreview(boundedPreview(event.Preview)))
	}
	if event.Name == "todo_write" && !failed {
		if block := todoProgress(event.Arguments); block != "" {
			line += "\n" + block
		}
	}
	return line
}

func summarizeArguments(arguments map[string]any, limit int) string {
	if len(arguments) == 0 {
		return ""
	}
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, min(3, len(keys)))
	for _, key := range keys {
		value, _ := json.Marshal(arguments[key])
		parts = append(parts, key+"="+string(value))
		if len(parts) == 3 {
			break
		}
	}
	result := strings.Join(parts, " ")
	if len(result) > limit {
		result = utf8Prefix(result, limit) + "…"
	}
	return result
}

func boundedPreview(value string) string {
	if len(value) > 2048 {
		value = utf8Prefix(value, 2048) + "…"
	}
	lines := strings.Split(value, "\n")
	if len(lines) > 8 {
		lines = append(lines[:8], "…")
	}
	return strings.Join(lines, "\n")
}

func utf8Prefix(value string, limit int) string {
	limit = min(max(0, limit), len(value))
	for limit > 0 && limit < len(value) && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return value[:limit]
}

func indentPreview(value string) string {
	return "  " + strings.ReplaceAll(value, "\n", "\n  ")
}

func todoProgress(arguments map[string]any) string {
	raw, _ := arguments["todos"].(string)
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var todos []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if json.Unmarshal([]byte(raw), &todos) != nil || len(todos) == 0 {
		return ""
	}
	lines := make([]string, 0, len(todos)+1)
	done := 0
	for _, todo := range todos {
		mark := "○"
		if todo.Status == "completed" {
			mark = "●"
			done++
		} else if todo.Status == "in_progress" {
			mark = "◐"
		}
		lines = append(lines, fmt.Sprintf("  %s %s", mark, todo.Content))
	}
	return fmt.Sprintf("任务进度 %d/%d\n%s", done, len(todos), strings.Join(lines, "\n"))
}
