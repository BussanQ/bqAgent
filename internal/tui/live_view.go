package tui

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type streamViewCache struct {
	text  string
	width int
	lines []string
}

func (model Model) streamLines(width int) []string {
	if model.stream == "" {
		return nil
	}
	cache := model.liveCache
	if cache != nil && cache.text == model.stream && cache.width == width {
		return cache.lines
	}
	lines := strings.Split(ansi.Hardwrap(model.stream, width, false), "\n")
	if cache != nil {
		cache.text, cache.width, cache.lines = model.stream, width, lines
	}
	return lines
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
	if model.stream != "" {
		reserved++
	}
	auxiliary = min(len(model.auxiliaryLines()), max(0, remaining-reserved))
	remaining -= auxiliary
	if len(model.tools.items) > 0 {
		tools = remaining
		if model.stream != "" {
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
// scrollback. Bound every live frame in physical rows, and keep the full text
// in model.stream for its single final history commit.
func (model Model) liveView() tea.View {
	width := model.contentWidth()
	toolHeight, streamHeight, auxiliaryHeight := model.liveBudgets()
	lines := append([]string{}, model.tools.viewport(model.styles, width, toolHeight)...)
	stream := model.streamLines(width)
	if streamHeight > 0 && len(stream) > 0 {
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
	padding := max(0, min(model.liveHeight, limit)-len(lines))
	if padding > 0 {
		lines = append(make([]string, padding), lines...)
		inputY += padding
	}
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
