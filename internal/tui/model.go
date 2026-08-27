package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

type phase uint8

const (
	phaseIdle phase = iota
	phaseThinking
	phaseStreaming
	phaseTool
	phaseCancelling
)

type activeTurn struct {
	sequence  uint64
	cancel    context.CancelFunc
	events    <-chan turnEvent
	cancelled bool
}

type queuedPrompt struct{ value string }

type suggestion struct {
	value       string
	description string
}

type Model struct {
	backend Backend
	config  Config
	styles  theme
	input   promptInput
	spinner spinner.Model
	width   int
	height  int

	phase        phase
	progress     string
	startedAt    time.Time
	stream       string
	active       *activeTurn
	sequence     uint64
	queued       *queuedPrompt
	pendingQuit  bool
	pendingClear bool

	sessionID  string
	runtime    RuntimeInfo
	metrics    *GenerationMetrics
	liveTokens int
	liveRunes  int

	historyStore *historyStore
	history      []string
	historyIndex int
	historyDraft string

	commands    []Command
	suggestions []suggestion
	suggested   int
	panelOpen   bool
	tools       toolGroup
	toolMouse   bool

	startup      []string
	initialTask  string
	initialSent  bool
	printQueue   []string
	quitArmedAt  time.Time
	statusNotice string
}

type startupMsg struct{}
type tickMsg time.Time

type turnEvent struct {
	sequence uint64
	kind     string
	text     string
	tool     ToolEvent
	result   TurnResult
	err      error
}

type controlResultMsg struct {
	result TurnResult
	err    error
}

func NewModel(backend Backend, config Config) Model {
	if config.Context == nil {
		config.Context = context.Background()
	}
	model := Model{
		backend:      backend,
		config:       config,
		styles:       newTheme(config.NoColor),
		input:        newPromptInput(config.NoColor),
		width:        82,
		phase:        phaseIdle,
		sessionID:    strings.TrimSpace(config.SessionID),
		initialTask:  strings.TrimSpace(config.InitialTask),
		historyStore: newHistoryStore(config.AgentDir, config.Workspace),
	}
	model.spinner = spinner.New(spinner.WithSpinner(spinner.Dot))
	if !config.NoColor {
		model.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(accentColor))
	}
	model.runtime = backend.RuntimeInfo(model.sessionID)
	model.commands = mergeCommands(backend.Commands())
	model.history, _ = model.historyStore.load()
	model.historyIndex = len(model.history)
	model.startup = model.startupLines()
	if model.sessionID != "" {
		if history, err := backend.History(model.sessionID, ResumeHistoryBudget); err == nil {
			model.startup = append(model.startup, model.renderHistory(history)...)
		} else {
			model.startup = append(model.startup, model.styles.error.Render("无法恢复会话："+err.Error()))
		}
	}
	return model
}

func (model Model) Init() tea.Cmd {
	return tea.Batch(model.input.area.Focus(), model.spinner.Tick, tickCommand(), func() tea.Msg { return startupMsg{} })
}

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	commands := make([]tea.Cmd, 0, 4)
	switch message := message.(type) {
	case startupMsg:
		model.enqueuePrint(model.startup...)
		if model.initialTask != "" && !model.initialSent {
			model.initialSent = true
			commands = append(commands, model.submit(model.initialTask))
		}
	case tea.WindowSizeMsg:
		model.width = message.Width
		model.height = message.Height
		model.resizeInput()
	case tea.KeyPressMsg:
		commands = append(commands, model.handleKey(message)...)
	case tea.MouseClickMsg:
		commands = append(commands, model.handleMouse(message)...)
	case turnEvent:
		commands = append(commands, model.handleTurnEvent(message)...)
	case controlResultMsg:
		if message.err != nil {
			model.enqueuePrint(model.styles.error.Render("停止命令失败：" + message.err.Error()))
		} else if strings.TrimSpace(message.result.Reply) != "" {
			model.enqueuePrint(renderMarkdown(message.result.Reply, model.contentWidth(), model.config.NoColor))
		}
	case spinner.TickMsg:
		var command tea.Cmd
		model.spinner, command = model.spinner.Update(message)
		commands = append(commands, command)
	case tickMsg:
		if !model.quitArmedAt.IsZero() && time.Since(model.quitArmedAt) > 3*time.Second {
			model.quitArmedAt = time.Time{}
			if model.statusNotice == "再按一次 Ctrl+C 退出" {
				model.statusNotice = ""
			}
		}
		commands = append(commands, tickCommand())
	}
	if len(model.printQueue) > 0 {
		text := strings.Join(model.printQueue, "\n")
		model.printQueue = nil
		return model, tea.Sequence(tea.Println(text), tea.Batch(commands...))
	}
	return model, tea.Batch(commands...)
}

func (model Model) View() tea.View {
	width := model.contentWidth()
	sections := make([]string, 0, 7)
	if tools := model.tools.render(model.styles, true); tools != "" {
		sections = append(sections, tools)
	}
	if model.stream != "" {
		sections = append(sections, renderStreaming(model.stream, width))
	}
	if model.panelOpen && len(model.suggestions) > 0 {
		sections = append(sections, model.renderSuggestions(width))
	}
	if model.queued != nil {
		preview := strings.ReplaceAll(strings.TrimSpace(model.queued.value), "\n", " ↵ ")
		if utf8.RuneCountInString(preview) > 100 {
			preview = string([]rune(preview)[:100]) + "…"
		}
		preview = ansi.Truncate(preview, max(4, width-9), "…")
		sections = append(sections, model.styles.dim.Render("已排队 · "+preview))
	}
	sections = append(sections, model.styles.dim.Render(strings.Repeat("─", max(10, width))))
	inputOffsetY := strings.Count(strings.Join(sections, "\n"), "\n") + 1
	sections = append(sections, model.input.area.View())
	sections = append(sections, model.renderStatus(width))
	view := tea.NewView(strings.Join(sections, "\n"))
	view.Cursor = model.input.area.Cursor()
	if view.Cursor != nil {
		view.Cursor.Position.Y += inputOffsetY
	}
	view.ReportFocus = true
	if model.toolMouse {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view
}

func (model *Model) handleKey(key tea.KeyPressMsg) []tea.Cmd {
	name := key.String()
	switch name {
	case "ctrl+t":
		model.toggleToolGroup()
		return nil
	case "ctrl+c":
		if model.active != nil {
			model.cancelActiveTurn()
			return nil
		}
		if model.panelOpen {
			model.panelOpen = false
			return nil
		}
		now := time.Now()
		if !model.quitArmedAt.IsZero() && now.Sub(model.quitArmedAt) <= 3*time.Second {
			return []tea.Cmd{model.commitToolGroup(), tea.Quit}
		}
		model.quitArmedAt = now
		model.statusNotice = "再按一次 Ctrl+C 退出"
		return nil
	case "ctrl+d":
		if model.active == nil && strings.TrimSpace(model.input.value()) == "" {
			return []tea.Cmd{model.commitToolGroup(), tea.Quit}
		}
		return nil
	case "ctrl+l":
		disableMouse := model.discardToolGroup()
		model.input.reset()
		model.closePanel()
		model.resizeInput()
		return []tea.Cmd{disableMouse, tea.ClearScreen}
	case "esc":
		if model.active != nil {
			model.cancelActiveTurn()
			return nil
		}
		if model.tools.expanded {
			model.tools.expanded = false
			return nil
		}
		model.closePanel()
		return nil
	case "alt+enter":
		command := model.input.update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model.refreshSuggestions()
		model.resizeInput()
		return []tea.Cmd{command}
	case "tab", "shift+tab":
		model.completeSuggestion(name == "shift+tab")
		return nil
	case "up":
		if model.panelOpen && len(model.suggestions) > 0 {
			model.suggested = (model.suggested - 1 + len(model.suggestions)) % len(model.suggestions)
			return nil
		}
		if model.input.area.Line() == 0 && model.recallHistory(-1) {
			return nil
		}
	case "down":
		if model.panelOpen && len(model.suggestions) > 0 {
			model.suggested = (model.suggested + 1) % len(model.suggestions)
			return nil
		}
		if model.input.area.Line()+1 == model.input.area.LineCount() && model.recallHistory(1) {
			return nil
		}
	case "enter":
		if model.panelOpen && len(model.suggestions) > 0 && strings.TrimSpace(model.input.displayValue()) != model.suggestions[model.suggested].value {
			model.input.setValue(model.suggestions[model.suggested].value)
			model.refreshSuggestions()
			return nil
		}
		value := strings.TrimSpace(model.input.value())
		if value == "" {
			return nil
		}
		model.input.reset()
		model.closePanel()
		model.resizeInput()
		if model.active != nil {
			return model.queue(value)
		}
		return []tea.Cmd{model.submit(value)}
	}
	command := model.input.update(key)
	model.refreshSuggestions()
	model.resizeInput()
	return []tea.Cmd{command}
}

func (model *Model) cancelActiveTurn() {
	model.active.cancelled = true
	model.active.cancel()
	model.phase = phaseCancelling
	model.progress = "正在取消"
}

func (model *Model) handleMouse(message tea.MouseClickMsg) []tea.Cmd {
	event := message.Mouse()
	if len(model.tools.items) <= toolCollapseThreshold || event.Button != tea.MouseLeft {
		return nil
	}
	viewLines := strings.Count(model.View().Content, "\n") + 1
	headerY := max(0, model.height-viewLines)
	if event.Y != headerY && !(model.height == 0 && event.Y == 0) {
		return nil
	}
	model.toggleToolGroup()
	return nil
}

func (model *Model) toggleToolGroup() {
	if len(model.tools.items) > toolCollapseThreshold {
		model.tools.expanded = !model.tools.expanded
	}
}

func (model *Model) submit(value string) tea.Cmd {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	lowerValue := strings.ToLower(value)
	if lowerValue == "/clear" {
		if model.active != nil {
			model.pendingClear = true
			model.active.cancelled = true
			model.active.cancel()
			model.phase = phaseCancelling
			return nil
		}
		disableMouse := model.discardToolGroup()
		model.clearSession()
		return tea.Batch(disableMouse, tea.ClearScreen)
	}
	toolCommand := model.commitToolGroup()
	switch lowerValue {
	case "/help":
		model.enqueuePrint(model.helpText())
		return toolCommand
	case "/exit":
		if model.active != nil {
			model.pendingQuit = true
			model.active.cancelled = true
			model.active.cancel()
			model.phase = phaseCancelling
			return nil
		}
		return tea.Batch(toolCommand, tea.Quit)
	}
	model.recordHistory(value)
	model.enqueueUserTurn(value)
	model.sequence++
	model.phase = phaseThinking
	model.progress = "正在思考"
	model.startedAt = time.Now()
	model.metrics = nil
	model.liveTokens = 0
	model.liveRunes = 0
	model.statusNotice = ""
	turnContext, cancel := context.WithCancel(model.config.Context)
	events := make(chan turnEvent, 256)
	model.active = &activeTurn{sequence: model.sequence, cancel: cancel, events: events}
	return tea.Batch(toolCommand, launchTurn(model.backend, turnContext, model.sequence, model.sessionID, value, events))
}

func (model *Model) queue(value string) []tea.Cmd {
	if strings.EqualFold(strings.TrimSpace(value), "/exit") {
		model.pendingQuit = true
		model.active.cancelled = true
		model.active.cancel()
		model.phase = phaseCancelling
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(value), "/clear") {
		model.pendingClear = true
		model.active.cancelled = true
		model.active.cancel()
		model.phase = phaseCancelling
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(value), "/stop") {
		model.recordHistory(value)
		model.enqueueUserTurn(value)
		return []tea.Cmd{runControl(model.backend, model.config.Context, model.sessionID, value)}
	}
	if model.queued == nil {
		model.queued = &queuedPrompt{value: value}
		model.statusNotice = "消息已排队"
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(value), "/") || strings.HasPrefix(strings.TrimSpace(model.queued.value), "/") {
		model.input.setValue(value)
		model.statusNotice = "Slash 命令需单独排队"
		return nil
	}
	model.queued.value += "\n" + value
	return nil
}

func (model *Model) handleTurnEvent(event turnEvent) []tea.Cmd {
	if event.sequence != model.sequence || model.active == nil || model.active.sequence != event.sequence {
		return nil
	}
	if event.kind != "done" {
		commands := []tea.Cmd{waitTurnEvent(model.active.events)}
		if model.active.cancelled {
			return commands
		}
		switch event.kind {
		case "token":
			model.phase = phaseStreaming
			model.progress = "正在生成"
			model.stream += event.text
			model.liveRunes += utf8.RuneCountInString(event.text)
			model.liveTokens = estimateTokensFromRunes(model.liveRunes)
		case "progress":
			if text := localizedProgress(event.text); text != "" {
				model.progress = text
			}
		case "tool":
			model.flushStream()
			model.phase = phaseTool
			model.progress = "正在运行工具 " + event.tool.Name
			if model.tools.add(event.tool) && !model.toolMouse {
				model.toolMouse = true
			}
		}
		return commands
	}
	cancelled := model.active.cancelled || errors.Is(event.err, context.Canceled)
	model.active = nil
	model.flushStream()
	if event.err != nil && !cancelled {
		model.enqueuePrint(model.styles.error.Render("本轮失败：" + event.err.Error()))
	} else if cancelled {
		model.enqueuePrint(model.styles.dim.Render("已取消本轮任务"))
	} else if (!event.result.Streamed || model.liveRunes == 0) && strings.TrimSpace(event.result.Reply) != "" {
		model.enqueuePrint(renderMarkdown(event.result.Reply, model.contentWidth(), model.config.NoColor))
	}
	if event.result.SessionID != "" {
		model.sessionID = event.result.SessionID
	}
	if event.result.Model != "" {
		model.runtime.Model = event.result.Model
	} else {
		model.runtime = model.backend.RuntimeInfo(model.sessionID)
	}
	model.metrics = event.result.Metrics
	model.phase = phaseIdle
	model.progress = ""
	if model.pendingClear {
		model.pendingClear = false
		disableMouse := model.discardToolGroup()
		model.clearSession()
		return []tea.Cmd{disableMouse, tea.ClearScreen}
	}
	if model.pendingQuit {
		return []tea.Cmd{model.commitToolGroup(), tea.Quit}
	}
	if model.queued != nil {
		queued := model.queued.value
		model.queued = nil
		if cancelled {
			if draft := strings.TrimSpace(model.input.value()); draft != "" {
				queued += "\n" + draft
			}
			model.input.setValue(queued)
			model.resizeInput()
			model.statusNotice = "已将排队内容恢复到输入框"
			return nil
		}
		return []tea.Cmd{model.submit(queued)}
	}
	return nil
}

func launchTurn(backend Backend, ctx context.Context, sequence uint64, sessionID, value string, events chan turnEvent) tea.Cmd {
	return func() tea.Msg {
		send := func(event turnEvent) {
			event.sequence = sequence
			select {
			case events <- event:
			case <-ctx.Done():
			}
		}
		sink := TurnEvents{
			Token:    func(text string) { send(turnEvent{kind: "token", text: text}) },
			Progress: func(text string) { send(turnEvent{kind: "progress", text: text}) },
			Tool:     func(event ToolEvent) { send(turnEvent{kind: "tool", tool: event}) },
		}
		go func() {
			result, err := backend.RunTurn(ctx, sessionID, value, sink)
			events <- turnEvent{sequence: sequence, kind: "done", result: result, err: err}
		}()
		return <-events
	}
}

func waitTurnEvent(events <-chan turnEvent) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func runControl(backend Backend, root context.Context, sessionID, value string) tea.Cmd {
	return func() tea.Msg {
		result, err := backend.RunTurn(root, sessionID, value, TurnEvents{})
		return controlResultMsg{result: result, err: err}
	}
}

func tickCommand() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(now time.Time) tea.Msg { return tickMsg(now) })
}

func (model *Model) flushStream() {
	if model.stream == "" {
		return
	}
	model.enqueuePrint(renderMarkdown(model.stream, model.contentWidth(), model.config.NoColor))
	model.stream = ""
}

func (model *Model) enqueueUserTurn(value string) {
	model.enqueuePrint(model.renderTurnSeparator(), model.renderUserMessage(value))
}

func (model Model) renderUserMessage(value string) string {
	return model.styles.user.Render("▸") + " " + strings.ReplaceAll(value, "\n", "\n  ")
}

func (model Model) renderTurnSeparator() string {
	return model.styles.dim.Render(strings.Repeat("─", max(10, model.contentWidth())))
}

func (model *Model) enqueuePrint(lines ...string) {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			model.printQueue = append(model.printQueue, line)
		}
	}
}

func (model *Model) commitToolGroup() tea.Cmd {
	if len(model.tools.items) == 0 {
		return nil
	}
	model.enqueuePrint(model.tools.render(model.styles, false))
	return model.discardToolGroup()
}

func (model *Model) discardToolGroup() tea.Cmd {
	model.tools = toolGroup{}
	model.toolMouse = false
	return nil
}

func (model *Model) clearSession() {
	model.sessionID = ""
	model.runtime = model.backend.RuntimeInfo("")
	model.metrics = nil
	model.liveTokens = 0
	model.liveRunes = 0
	model.queued = nil
	model.stream = ""
	model.tools = toolGroup{}
	model.toolMouse = false
	model.input.reset()
	model.closePanel()
	model.printQueue = nil
	model.statusNotice = "已清理显示；下一条消息将创建新会话"
}

func (model *Model) recordHistory(value string) {
	model.history, _ = model.historyStore.append(model.history, value)
	model.historyIndex = len(model.history)
	model.historyDraft = ""
}

func (model *Model) recallHistory(direction int) bool {
	if len(model.history) == 0 {
		return false
	}
	if direction < 0 {
		if model.historyIndex == len(model.history) {
			model.historyDraft = model.input.value()
		}
		if model.historyIndex == 0 {
			return true
		}
		model.historyIndex--
		model.input.setValue(model.history[model.historyIndex])
		return true
	}
	if model.historyIndex >= len(model.history) {
		return false
	}
	model.historyIndex++
	if model.historyIndex == len(model.history) {
		model.input.setValue(model.historyDraft)
	} else {
		model.input.setValue(model.history[model.historyIndex])
	}
	return true
}

func (model *Model) refreshSuggestions() {
	rawValue := model.input.displayValue()
	value := strings.TrimSpace(rawValue)
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "\n") {
		model.closePanel()
		return
	}
	model.commands = mergeCommands(model.backend.Commands())
	fields := strings.Fields(value)
	trailingSpace := strings.HasSuffix(rawValue, " ")
	suggestions := make([]suggestion, 0)
	if len(fields) <= 1 && !trailingSpace {
		for _, command := range model.commands {
			if strings.HasPrefix(strings.ToLower(command.Name), strings.ToLower(value)) {
				suggestions = append(suggestions, suggestion{value: command.Name, description: command.Description})
			}
		}
	} else if len(fields) >= 1 {
		prefix := ""
		if len(fields) > 1 && !trailingSpace {
			prefix = fields[len(fields)-1]
		}
		for _, command := range model.commands {
			if !strings.EqualFold(command.Name, fields[0]) {
				continue
			}
			for _, argument := range command.Arguments {
				if strings.HasPrefix(strings.ToLower(argument.Value), strings.ToLower(prefix)) {
					suggestions = append(suggestions, suggestion{value: command.Name + " " + argument.Value, description: argument.Description})
				}
			}
		}
	}
	if len(suggestions) > 8 {
		suggestions = suggestions[:8]
	}
	model.suggestions = suggestions
	model.panelOpen = len(suggestions) > 0
	if model.suggested >= len(suggestions) {
		model.suggested = 0
	}
}

func (model *Model) completeSuggestion(reverse bool) {
	if !model.panelOpen || len(model.suggestions) == 0 {
		if strings.TrimSpace(model.input.value()) == "" {
			model.input.setValue("/")
			model.refreshSuggestions()
		}
		return
	}
	if reverse {
		model.suggested = (model.suggested - 1 + len(model.suggestions)) % len(model.suggestions)
	} else if strings.TrimSpace(model.input.displayValue()) == model.suggestions[model.suggested].value {
		model.suggested = (model.suggested + 1) % len(model.suggestions)
	}
	model.input.setValue(model.suggestions[model.suggested].value)
	model.refreshSuggestions()
}

func (model *Model) closePanel() {
	model.panelOpen = false
	model.suggestions = nil
	model.suggested = 0
}

func (model Model) renderSuggestions(width int) string {
	lines := make([]string, 0, len(model.suggestions)+1)
	lines = append(lines, model.styles.accent.Render("命令与参数"))
	for index, item := range model.suggestions {
		pointer := "  "
		if index == model.suggested {
			pointer = "› "
		}
		line := pointer + item.value
		if item.description != "" && width > 40 {
			line += model.styles.dim.Render("  " + item.description)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (model Model) renderStatus(width int) string {
	left := model.runtime.Provider
	if left == "" {
		left = string(model.runtime.APIType)
	}
	if left == "" {
		left = "provider"
	}
	left += "/" + model.runtime.Model
	if model.sessionID != "" {
		id := model.sessionID
		if len(id) > 8 {
			id = id[:8]
		}
		left += " · " + id
	}
	right := model.statusNotice
	if model.phase != phaseIdle {
		elapsed := time.Since(model.startedAt).Round(100 * time.Millisecond)
		right = fmt.Sprintf("%s %s · %s · ~%d tok", model.spinner.View(), model.phaseLabel(), elapsed, model.liveTokens)
	} else if model.metrics != nil {
		parts := []string{
			fmt.Sprintf("首字 %.2fs", float64(model.metrics.FirstTokenLatencyMS)/1000),
			fmt.Sprintf("%d tok", model.metrics.CompletionTokens),
		}
		if model.metrics.CacheMetrics != nil && model.metrics.CacheMetrics.Available && model.metrics.CacheMetrics.InputTokens > 0 {
			rate := 100 * model.metrics.CacheMetrics.HitRate
			rate = min(100, max(0, rate))
			parts = append(parts, fmt.Sprintf("缓存命中 %.0f%%", rate))
		} else if model.metrics.CacheUsageAvailable && model.metrics.PromptTokens > 0 {
			rate := 100 * float64(model.metrics.CachedPromptTokens) / float64(model.metrics.PromptTokens)
			rate = min(100, max(0, rate))
			parts = append(parts, fmt.Sprintf("缓存命中 %.0f%%", rate))
		}
		parts = append(parts, fmt.Sprintf("%.1f tok/s", model.metrics.TokensPerSecond))
		right = strings.Join(parts, " · ")
	}
	if right == "" {
		right = "Enter 发送 · Alt+Enter 换行 · / 命令"
	}
	left = ansi.Truncate(left, width, "…")
	right = ansi.Truncate(right, width, "…")
	space := width - runewidth.StringWidth(left) - lipgloss.Width(right)
	if space < 1 {
		return model.styles.dim.Render(left + "\n" + right)
	}
	return model.styles.dim.Render(left + strings.Repeat(" ", space) + right)
}

func (model Model) phaseLabel() string {
	if model.progress != "" {
		return model.progress
	}
	switch model.phase {
	case phaseThinking:
		return "正在思考"
	case phaseStreaming:
		return "正在生成"
	case phaseTool:
		return "正在运行工具"
	case phaseCancelling:
		return "正在取消"
	default:
		return "就绪"
	}
}

func (model *Model) resizeInput() {
	lines := model.input.area.LineCount()
	maxHeight := min(6, max(1, model.height-5))
	model.input.resize(model.contentWidth(), min(maxHeight, max(1, lines)))
}

func (model Model) contentWidth() int { return max(10, model.width-2) }

func (model Model) startupLines() []string {
	modelName := model.runtime.Model
	if modelName == "" {
		modelName = "未配置"
	}
	provider := model.runtime.Provider
	if provider == "" {
		provider = model.runtime.APIType
	}
	if provider == "" {
		provider = "未配置"
	}
	return []string{
		model.styles.accent.Render("bqAgent") + "  Harness",
		model.styles.dim.Render("工具可能修改文件或运行命令，请留意工具摘要与工作区边界。"),
		fmt.Sprintf("工作区  %s\n模型    %s/%s", model.config.Workspace, provider, modelName),
	}
}

func (model Model) renderHistory(history History) []string {
	lines := []string{model.styles.dim.Render("恢复会话 " + history.ID)}
	if history.Omitted > 0 {
		lines = append(lines, model.styles.dim.Render(fmt.Sprintf("已省略更早的 %d 条消息（恢复显示上限 200 KiB）", history.Omitted)))
	}
	for _, message := range history.Messages {
		if message.Role == "user" {
			lines = append(lines, model.renderTurnSeparator(), model.renderUserMessage(message.Content))
		} else {
			lines = append(lines, renderMarkdown(message.Content, model.contentWidth(), model.config.NoColor))
		}
	}
	return lines
}

func (model Model) helpText() string {
	return `快捷键
  Enter 发送    Alt+Enter 换行    Tab/Shift+Tab 补全
  ↑/↓ 历史      Ctrl+C 取消/双击退出    Ctrl+D 空闲退出
  Esc 打断当前回复；空闲时关闭面板/收起工具详情
  Ctrl+L 清理当前视口和草稿（保留 Session）
  Ctrl+T 展开/收起已合并的工具详情（也可点击工具汇总行）

本地命令
  /help 帮助    /clear 新建会话并清理显示    /exit 安全退出

后端命令
  /model  /skill  /memory  /feedback  /agent
  /claude  /codex  /cursor  /opencode  /default  /stop`
}

func mergeCommands(dynamic []Command) []Command {
	base := []Command{
		{Name: "/help", Description: "显示快捷键和命令帮助"},
		{Name: "/clear", Description: "清理显示并在下一条消息创建新会话"},
		{Name: "/exit", Description: "安全退出"},
		{Name: "/model", Description: "查看或切换模型"},
		{Name: "/skill", Description: "使用 Skill 或 alias"},
		{Name: "/memory", Description: "管理记忆", Arguments: []CommandArgument{{Value: "list", Description: "列出记忆"}, {Value: "search", Description: "搜索记忆"}, {Value: "confirm", Description: "确认候选记忆"}, {Value: "compact", Description: "压缩记忆"}}},
		{Name: "/feedback", Description: "提交运行反馈", Arguments: []CommandArgument{{Value: "up", Description: "正向反馈"}, {Value: "down", Description: "负向反馈"}}},
		{Name: "/agent", Description: "管理子代理", Arguments: []CommandArgument{{Value: "spawn", Description: "启动子代理"}, {Value: "list", Description: "列出子代理"}, {Value: "status", Description: "查看状态"}, {Value: "wait", Description: "等待结果"}, {Value: "interrupt", Description: "中断"}, {Value: "cancel", Description: "取消"}, {Value: "resume", Description: "恢复"}, {Value: "result", Description: "查看结果"}, {Value: "collect", Description: "收集结果"}, {Value: "apply", Description: "应用变更"}, {Value: "cleanup", Description: "清理"}}},
		{Name: "/claude", Description: "切换到 Claude 外部 Agent"},
		{Name: "/codex", Description: "切换到 Codex 外部 Agent"},
		{Name: "/cursor", Description: "切换到 Cursor 外部 Agent"},
		{Name: "/opencode", Description: "切换到 OpenCode 外部 Agent"},
		{Name: "/default", Description: "恢复默认 Agent"},
		{Name: "/stop", Description: "停止当前外部 Agent 进程"},
	}
	byName := make(map[string]int, len(base))
	for index, command := range base {
		byName[strings.ToLower(command.Name)] = index
	}
	for _, command := range dynamic {
		key := strings.ToLower(command.Name)
		if index, ok := byName[key]; ok {
			if len(command.Arguments) > 0 {
				base[index].Arguments = command.Arguments
			}
			if command.Description != "" {
				base[index].Description = command.Description
			}
			continue
		}
		byName[key] = len(base)
		base = append(base, command)
	}
	return base
}

func estimateTokens(value string) int { return estimateTokensFromRunes(utf8.RuneCountInString(value)) }

func estimateTokensFromRunes(runes int) int {
	if runes == 0 {
		return 0
	}
	return max(1, runes/4)
}

func localizedProgress(value string) string {
	lower := strings.ToLower(value)
	for _, marker := range []string{"assembling", "boiling", "calculating", "coalescing", "smooshing", "distilling", "formatting", "exploring"} {
		if strings.Contains(lower, marker) {
			return "正在等待模型"
		}
	}
	if strings.Contains(lower, "stage") {
		return "正在处理阶段"
	}
	return ""
}
