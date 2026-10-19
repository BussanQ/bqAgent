package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
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
		user:   lipgloss.NewStyle().Foreground(lipgloss.Color("#74C0FC")).Bold(true),
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

func (model Model) renderFinalReply(value string, width int) string {
	style := styles.DarkStyleConfig
	if model.config.NoColor {
		style = styles.NoTTYStyleConfig
	}
	// Own the outer indentation for the whole answer. Removing whitespace
	// from individual lines would also destroy nested lists and code indentation.
	zero := uint(0)
	style.Document.Margin = &zero
	style.CodeBlock.Margin = &zero
	style.H1.Prefix = ""
	bodyWidth := max(1, width-2)
	rendered := strings.TrimSpace(value)
	if renderer, err := glamour.NewTermRenderer(glamour.WithStyles(style), glamour.WithWordWrap(bodyWidth)); err == nil {
		if result, err := renderer.Render(rendered); err == nil {
			rendered = result
		}
	}
	lines := strings.Split(ansi.Hardwrap(rendered, bodyWidth, false), "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		prefix := "  "
		if i == 0 {
			prefix = model.styles.ok.Render("●") + " "
		}
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func toolSummary(event ToolEvent, styles theme, width int, expanded bool) string {
	width = max(1, width)
	sequence := event.Seq
	if event.Kind == "tool_start" {
		line := fmt.Sprintf("%s %s %s", styles.accent.Render(fmt.Sprintf("⚙ #%d", sequence)), event.Name, summarizeArguments(event.Arguments, 180))
		line = ansi.Truncate(line, width, "…")
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
	preview := ""
	clipped := false
	if (failed || expanded) && strings.TrimSpace(event.Preview) != "" {
		if expanded {
			preview = plainToolOutput(event.Preview)
		} else {
			// Account for preview indentation before bounding physical rows.
			preview, clipped = boundedPreview(event.Preview, max(1, width-2))
		}
	}
	if event.Truncated || clipped {
		line += " · 输出已截断"
	}
	line = ansi.Truncate(line, width, "…")
	if preview != "" {
		// Only the failure marker is red: a recoverable tool error is not a
		// failed turn, and command-provided ANSI must not repaint the transcript.
		line += "\n" + styles.dim.Render(indentPreview(preview))
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

const toolPreviewLines = 3

func turnFailureSummary(message string, styles theme, width int) string {
	preview, clipped := boundedPreview(message, width)
	heading := "本轮失败："
	if clipped {
		heading += "错误详情已截断"
	}
	return styles.error.Render(ansi.Truncate(heading, width, "…")) + "\n" + styles.dim.Render(preview)
}

func plainToolOutput(value string) string {
	value = ansi.Strip(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\t", "    ")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
}

func boundedPreview(value string, width int) (string, bool) {
	width = max(1, width)
	lines := strings.Split(ansi.Hardwrap(plainToolOutput(value), width, true), "\n")
	clipped := len(lines) > toolPreviewLines
	if clipped {
		lines = lines[:toolPreviewLines]
		// The ellipsis belongs to the third row, never an extra fourth row.
		lines[toolPreviewLines-1] = ansi.Truncate(lines[toolPreviewLines-1], width-1, "") + "…"
	}
	return strings.Join(lines, "\n"), clipped
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
