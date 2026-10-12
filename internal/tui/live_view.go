package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type streamViewCache struct {
	text  string
	width int
	lines []string
	final bool
}

func (model Model) streamLines(width int) []string {
	text := model.stream
	final := model.reply != ""
	if final {
		text = model.reply
	}
	if text == "" {
		return nil
	}
	cache := model.liveCache
	if cache != nil && cache.text == text && cache.width == width && cache.final == final {
		return cache.lines
	}
	rendered := text
	if final {
		rendered = renderMarkdown(text, width, model.config.NoColor)
	}
	lines := strings.Split(ansi.Hardwrap(rendered, width, false), "\n")
	if cache != nil {
		cache.text, cache.width, cache.lines, cache.final = text, width, lines, final
	}
	return lines
}

func (model Model) replyViewport(lines []string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if len(lines) <= height {
		return lines
	}
	count := max(1, height-1)
	start := min(model.replyOffset, len(lines)-count)
	visible := append([]string{}, lines[start:start+count]...)
	if height > 1 {
		hint := fmt.Sprintf("结论 %d–%d/%d · PgUp/PgDn 翻页", start+1, start+count, len(lines))
		if model.tools.expanded {
			hint = fmt.Sprintf("结论 %d–%d/%d · 收起工具后 PgUp/PgDn 翻页", start+1, start+count, len(lines))
		}
		visible = append(visible, model.styles.dim.Render(ansi.Truncate(hint, width, "…")))
	}
	return visible
}

func (model Model) frameLimit() int {
	if model.height <= 0 {
		return 23 // Conservative until the terminal reports its dimensions.
	}
	return max(1, model.height-1)
}

func (model Model) footerLines() []string {
	width := model.contentWidth()
	lines := []string{model.styles.dim.Render(strings.Repeat("─", width))}
	lines = append(lines, strings.Split(model.input.area.View(), "\n")...)
	lines = append(lines, strings.Split(model.renderStatus(width), "\n")...)
	// Status labels and textarea decorations must not create implicit wraps.
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return lines
}

func (model Model) auxiliaryLines() []string {
	width := model.contentWidth()
	var lines []string
	if model.panelOpen && len(model.suggestions) > 0 {
		lines = strings.Split(model.renderSuggestions(width), "\n")
	}
	if model.queued != nil {
		preview := strings.ReplaceAll(strings.TrimSpace(model.queued.value), "\n", " ↵ ")
		if utf8.RuneCountInString(preview) > 100 {
			preview = string([]rune(preview)[:100]) + "…"
		}
		lines = append(lines, model.styles.dim.Render("已排队 · "+preview))
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return lines
}

func (model Model) liveBudgets() (tools, stream, auxiliary int) {
	footer := model.footerLines()
	remaining := max(0, model.frameLimit()-len(footer))
	reserved := 0
	if len(model.tools.items) > 0 {
		reserved++
	}
	if model.stream != "" || model.reply != "" {
		reserved++
	}
	auxiliary = min(len(model.auxiliaryLines()), max(0, remaining-reserved))
	remaining -= auxiliary
	if len(model.tools.items) > 0 {
		tools = remaining
		if model.stream != "" || model.reply != "" {
			tools = max(1, remaining/2)
		}
		tools = min(tools, len(model.tools.viewLines(model.styles, model.contentWidth())))
		tools = min(tools, remaining)
	}
	stream = remaining - tools
	return
}

func (model Model) toolViewportHeight() int {
	height, _, _ := model.liveBudgets()
	return height
}

// The inline renderer cannot erase rows that have escaped into native
// scrollback. Bound every live frame in physical rows, and retain the complete
// stream or final reply until its single history commit.
func (model Model) liveView() tea.View {
	width := model.contentWidth()
	toolHeight, streamHeight, auxiliaryHeight := model.liveBudgets()
	lines := append([]string{}, model.tools.viewport(model.styles, width, toolHeight)...)
	stream := model.streamLines(width)
	if model.reply != "" {
		lines = append(lines, model.replyViewport(stream, width, streamHeight)...)
	} else if streamHeight > 0 && len(stream) > 0 {
		lines = append(lines, stream[max(0, len(stream)-streamHeight):]...)
	}
	auxiliary := model.auxiliaryLines()
	lines = append(lines, auxiliary[:auxiliaryHeight]...)
	inputY := len(lines) + 1
	footer := model.footerLines()
	lines = append(lines, footer...)
	limit := model.frameLimit()
	if len(lines) > limit {
		inputY -= len(lines) - limit
		lines = lines[len(lines)-limit:]
	}
	// Use the current content height so collapsed tools occupy only the summary row.
	view := tea.NewView(strings.Join(lines, "\n"))
	view.Cursor = model.input.area.Cursor()
	if view.Cursor != nil {
		view.Cursor.Position.Y += inputY
		if view.Cursor.Position.Y < 0 || view.Cursor.Position.Y >= len(lines) || view.Cursor.Position.X >= width {
			view.Cursor = nil
		}
	}
	view.ReportFocus = true
	if model.toolMouse {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view
}
