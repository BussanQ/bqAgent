package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"bqagent/internal/turn"

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
	if final && !model.replyRendered {
		// Add the marker before splitting history and the live suffix, so it
		// belongs to the answer exactly once rather than to every repaint.
		rendered = model.renderFinalReply(text, width)
	}
	lines := strings.Split(ansi.Hardwrap(rendered, width, false), "\n")
	if cache != nil {
		cache.text, cache.width, cache.lines, cache.final = text, width, lines, final
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
	lines = append(lines, model.styles.dim.Render(ansi.Truncate("工作区  "+model.config.Workspace, width, "…")))
	// Status labels and textarea decorations must not create implicit wraps.
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return lines
}

func (model Model) auxiliaryLines() []string {
	width := model.contentWidth()
	var lines []string
	if model.panelOpen && (len(model.suggestions) > 0 || model.mention != nil) {
		lines = append(lines, strings.Split(model.renderSuggestions(width), "\n")...)
	}
	if model.active != nil {
		for i, input := range model.active.pending {
			if i == 3 {
				lines = append(lines, model.styles.dim.Render(fmt.Sprintf("…还有 %d 条待处理", len(model.active.pending)-i)))
				break
			}
			label := "引导"
			if input.kind == turn.FollowUp {
				label = "跟进"
			}
			lines = append(lines, model.styles.dim.Render("待处理 · "+label+" · "+previewText(input.text)))
		}
	}
	if model.queued != nil {
		preview := previewText(model.queued.value)
		lines = append(lines, model.styles.dim.Render("已排队 · "+preview))
	}
	for _, line := range model.task.lines() {
		lines = append(lines, model.styles.dim.Render(line))
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
	if model.tools.liveVisible() {
		reserved++
	}
	if model.stream != "" || model.reply != "" {
		reserved++
	}
	auxiliary = min(len(model.auxiliaryLines()), max(0, remaining-reserved))
	remaining -= auxiliary
	if model.tools.liveVisible() {
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

// Keep recent committed rows in the frame. Only rows displaced by live content
// enter native scrollback, so filling the terminal does not hide short turns.
func (model *Model) flushTranscript() string {
	width := model.contentWidth()
	for _, text := range model.printQueue {
		model.transcript = append(model.transcript, strings.Split(ansi.Hardwrap(text, width, false), "\n")...)
	}
	model.printQueue = nil
	if len(model.transcript) == 0 || model.toolInspector {
		return ""
	}
	// Rewrap retained rows when the terminal narrows; already printed rows are
	// owned by the terminal and must never be emitted again.
	rows := strings.Split(ansi.Hardwrap(strings.Join(model.transcript, "\n"), width, false), "\n")
	capacity := max(0, model.frameLimit()-len(model.liveLines()))
	cut := max(0, len(rows)-capacity)
	model.transcript = append([]string(nil), rows[cut:]...)
	if cut == 0 {
		return ""
	}
	text := strings.Join(rows[:cut], "\n")
	if text == "" {
		return " "
	}
	return text
}

func (model Model) liveLines() []string {
	width := model.contentWidth()
	toolHeight, streamHeight, auxiliaryHeight := model.liveBudgets()
	tools := model.tools.viewport(model.styles, width, toolHeight)
	var lines []string
	if !model.tools.committed {
		lines = append(lines, tools...)
	}
	stream := model.streamLines(width)
	if model.reply != "" {
		lines = append(lines, stream...)
	} else if streamHeight > 0 && len(stream) > 0 {
		lines = append(lines, stream[max(0, len(stream)-streamHeight):]...)
	}
	auxiliary := model.auxiliaryLines()
	lines = append(lines, auxiliary[:auxiliaryHeight]...)
	lines = append(lines, model.footerLines()...)
	limit := model.frameLimit()
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// Keep retained conversation, tools and replies together at the top, while
// auxiliary panels and the input footer stay at the bottom. Return the tool header row for mouse hits.
func (model Model) liveLayout() (lines []string, toolRow int) {
	body := model.liveLines()
	padding := model.frameLimit() - len(body)
	retained := model.transcript[max(0, len(model.transcript)-padding):]
	toolHeight, _, auxiliaryHeight := model.liveBudgets()
	toolRow = -1
	if !model.tools.committed && toolHeight > 0 {
		toolRow = len(retained)
	}
	contentEnd := max(0, len(body)-auxiliaryHeight-len(model.footerLines()))
	lines = make([]string, len(retained), model.frameLimit())
	copy(lines, retained)
	lines = append(lines, body[:contentEnd]...)
	gapStart := len(lines)
	lines = append(lines, make([]string, padding-len(retained))...)
	if model.sequence == 0 && model.sessionID == "" && model.active == nil && len(retained) > 0 && len(model.tools.items) == 0 && model.stream == "" && model.reply == "" {
		model.drawStartupArt(lines[gapStart:])
	}
	lines = append(lines, body[contentEnd:]...)
	return
}

// A constant physical height lets the renderer scroll changed rows without
// resizing and erasing its buffer on every token or tool collapse. Leave one
// terminal row for native history insertion.
func (model Model) liveView() tea.View {
	width := model.contentWidth()
	lines, _ := model.liveLayout()
	inputY := len(lines) - len(model.footerLines()) + 1
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

func previewText(value string) string {
	preview := strings.ReplaceAll(strings.TrimSpace(value), "\n", " ↵ ")
	if utf8.RuneCountInString(preview) > 100 {
		preview = string([]rune(preview)[:100]) + "…"
	}
	return preview
}
