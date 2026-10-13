package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"bqagent/internal/chatcommand"
	"bqagent/internal/mcp"
	"bqagent/internal/modelcatalog"
	"bqagent/internal/turn"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	label       string
	value       string
	description string
	confirm     bool
}

const maxSuggestions = 8

const quitConfirmWindow = 2 * time.Second
const quitConfirmHint = "再次按 Ctrl+C 退出（2 秒内）"

type Model struct {
	messages turn.Messages
	backend  Backend
	config   Config
	styles   theme
	input    promptInput
	spinner  spinner.Model
	width    int
	height   int

	phase            phase
	progress         string
	startedAt        time.Time
	stream           string
	reply            string
	replyRendered    bool
	active           *activeTurn
	sequence         uint64
	queued           *queuedPrompt
	pendingQuit      bool
	pendingClear     bool
	quitConfirmUntil time.Time

	sessionID    string
	runtime      RuntimeInfo
	metrics      *GenerationMetrics
	usage        *TokenUsage
	contextUsage *turn.ContextUsage
	liveTokens   int
	liveRunes    int

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
	liveCache   *streamViewCache
	provider    *providerWizard

	startup       []string
	initialTask   string
	initialSent   bool
	printQueue    []string
	historyPrints int
	historyQueue  []historyPrint
	statusNotice  string
	mcpStatus     []mcp.ServerStatus
}

type historyPrint struct {
	text  string
	after tea.Cmd
}

type startupMsg struct{}
type tickMsg time.Time

// historyPrintedMsg follows the native history insertion, allowing the inline
// wrapper to publish a shorter frame without erasing the newly inserted text.
type historyPrintedMsg struct{}

type turnEvent struct {
	message      turn.Event
	sequence     uint64
	kind         string
	text         string
	tool         ToolEvent
	usage        TokenUsage
	contextUsage turn.ContextUsage
	result       TurnResult
	err          error
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
		liveCache:    &streamViewCache{},
	}
	model.spinner = spinner.New(spinner.WithSpinner(spinner.Dot))
	if !config.NoColor {
		model.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(accentColor))
	}
	model.refreshMCPStatus()
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
	return tea.Batch(model.input.area.Focus(), tickCommand(), func() tea.Msg { return startupMsg{} })
}

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	commands := make([]tea.Cmd, 0, 4)
	switch message := message.(type) {
	case historyPrintedMsg:
		model.historyPrints = max(0, model.historyPrints-1)
	case startupMsg:
		model.reportActivity("idle")
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
		model.quitConfirmUntil = time.Time{}
		commands = append(commands, model.handleMouse(message)...)
	case turnEvent:
		commands = append(commands, model.handleTurnEvent(message)...)
	case providerSettingsMsg:
		commands = append(commands, model.handleProviderSettings(message)...)
	case providerModelsMsg:
		commands = append(commands, model.handleProviderModels(message)...)
	case providerSavedMsg:
		commands = append(commands, model.handleProviderSaved(message)...)
	case controlResultMsg:
		if message.err != nil {
			model.enqueuePrint(model.styles.error.Render("停止命令失败：" + message.err.Error()))
		} else if strings.TrimSpace(message.result.Reply) != "" {
			model.enqueuePrint(renderMarkdown(message.result.Reply, model.contentWidth(), model.config.NoColor))
		}
	case spinner.TickMsg:
		if model.active != nil {
			var command tea.Cmd
			model.spinner, command = model.spinner.Update(message)
			commands = append(commands, command)
		}
	case tickMsg:
		model.refreshMCPStatus()
		if !time.Time(message).Before(model.quitConfirmUntil) {
			model.quitConfirmUntil = time.Time{}
		}
		commands = append(commands, tickCommand())
	default:
		if _, pasted := message.(tea.PasteMsg); pasted {
			model.quitConfirmUntil = time.Time{}
		}
		if model.provider != nil {
			commands = append(commands, model.provider.updateInput(message))
			break
		}
		// Textarea clipboard reads complete asynchronously. Forward messages
		// unknown to the model so Ctrl+V and bracketed paste reach the input.
		command := model.input.update(message)
		commands = append(commands, command)
		model.refreshSuggestions()
		model.resizeInput()
	}
	model.flushOverflowReply()
	if len(model.printQueue) > 0 {
		model.historyQueue = append(model.historyQueue, historyPrint{
			text: strings.Join(model.printQueue, "\n"), after: tea.Batch(commands...),
		})
		model.printQueue = nil
		commands = nil
	}
	if model.historyPrints == 0 && len(model.historyQueue) > 0 {
		// A new prompt may arrive while a long answer is being printed. Finish
		// the current batch before inserting the next one, preserving turn order.
		batch := model.historyQueue[0]
		model.historyQueue = model.historyQueue[1:]
		model.historyPrints++
		commands = append(commands, tea.Sequence(printHistoryLines(batch.text, model.contentWidth()), func() tea.Msg { return historyPrintedMsg{} }, batch.after))
	}
	return model, tea.Batch(commands...)
}

func (model Model) View() tea.View {
	if model.provider != nil {
		content, cursor := model.provider.render(model.styles)
		if model.quitConfirmationActive() {
			content += "\n" + model.styles.dim.Render(quitConfirmHint)
		}
		view := tea.NewView(content)
		view.Cursor = cursor
		view.ReportFocus = true
		return view
	}
	return model.liveView()
}

func (model *Model) handleKey(key tea.KeyPressMsg) []tea.Cmd {
	name := key.String()
	if name == "ctrl+c" {
		if model.quitConfirmationActive() {
			return model.requestQuit()
		}
		model.quitConfirmUntil = time.Now().Add(quitConfirmWindow)
	} else {
		model.quitConfirmUntil = time.Time{}
	}
	if model.provider != nil {
		return model.handleProviderKey(key)
	}
	switch name {
	case "pgup", "pgdown":
		if len(model.tools.items) > 0 && (model.tools.expanded || len(model.tools.items) <= toolCollapseThreshold) {
			delta := max(1, model.toolViewportHeight()-2)
			if name == "pgup" {
				delta = -delta
			}
			lines := model.tools.viewLines(model.styles, model.contentWidth())
			limit := max(0, len(lines)-1-max(1, model.toolViewportHeight()-2))
			model.tools.offset = min(limit, max(0, min(model.tools.offset, limit)+delta))
			return nil
		}
	case "ctrl+o":
		model.toggleToolGroup()
		return nil
	case "ctrl+q":
		return model.requestQuit()
	case "ctrl+l":
		disableMouse := model.discardToolGroup()
		model.reply = ""
		model.replyRendered = false
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
			if model.tools.committed {
				model.toolMouse = false
			}
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
			selected := model.suggestions[model.suggested]
			model.input.setValue(selected.value)
			if !selected.confirm {
				model.refreshSuggestions()
				return nil
			}
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

func (model Model) quitConfirmationActive() bool {
	return !model.quitConfirmUntil.IsZero() && time.Now().Before(model.quitConfirmUntil)
}

func (model *Model) requestQuit() []tea.Cmd {
	model.quitConfirmUntil = time.Time{}
	model.pendingClear = false
	if model.active != nil {
		model.pendingQuit = true
		model.active.cancelled = true
		model.active.cancel()
		model.phase = phaseCancelling
		model.progress = "正在安全退出"
		return nil
	}
	model.provider = nil
	return []tea.Cmd{model.commitTurn(), tea.Quit}
}

func (model *Model) cancelActiveTurn() {
	model.active.cancelled = true
	model.active.cancel()
	model.phase = phaseCancelling
	model.progress = "正在取消"
}

func (model *Model) handleMouse(message tea.MouseClickMsg) []tea.Cmd {
	event := message.Mouse()
	if (!model.tools.committed && len(model.tools.items) <= toolCollapseThreshold) || event.Button != tea.MouseLeft || model.toolViewportHeight() == 0 {
		return nil
	}
	viewLines := strings.Split(model.View().Content, "\n")
	headerY := max(0, model.height-len(viewLines))
	if model.tools.committed {
		toolHeight, _, auxiliaryHeight := model.liveBudgets()
		panelRows := len(model.tools.viewport(model.styles, model.contentWidth(), toolHeight))
		headerRow := len(viewLines) - len(model.footerLines()) - auxiliaryHeight - panelRows
		if headerRow < 0 {
			return nil
		}
		headerY += headerRow
	}
	if event.Y != headerY && !(model.height == 0 && event.Y == 0) {
		return nil
	}
	model.toggleToolGroup()
	return nil
}

func (model *Model) toggleToolGroup() {
	if len(model.tools.items) > toolCollapseThreshold || model.tools.committed {
		model.tools.expanded = !model.tools.expanded
		model.tools.offset = 0
		if model.tools.committed {
			model.toolMouse = model.tools.expanded
		}
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
	toolCommand := model.commitTurn()
	switch lowerValue {
	case "/help":
		model.enqueuePrint(model.helpText())
		return toolCommand
	case "/provider":
		model.reportActivity("blocked")
		model.provider = newProviderWizard(model.config.NoColor, model.contentWidth())
		model.input.area.Blur()
		return tea.Batch(toolCommand, loadProviderSettings(model.backend, model.config.Context))
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
	model.messages = turn.Messages{}
	model.reportActivity("working")
	model.progress = "正在思考"
	model.startedAt = time.Now()
	model.metrics = nil
	model.usage = nil
	model.liveTokens = 0
	model.liveRunes = 0
	model.statusNotice = ""
	turnContext, cancel := context.WithCancel(model.config.Context)
	events := make(chan turnEvent, 256)
	model.active = &activeTurn{sequence: model.sequence, cancel: cancel, events: events}
	return tea.Batch(toolCommand, model.spinner.Tick, launchTurn(model.backend, turnContext, model.sequence, model.sessionID, value, events))
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
		case "context":
			usage := event.contextUsage
			model.contextUsage = &usage
		case "session":
			model.sessionID = event.text
			model.reportActivity("working")
		case "usage":
			usage := event.usage
			model.usage = &usage
		case "message":
			before := utf8.RuneCountInString(model.messages.Items[event.message.ItemID].Text)
			if model.messages.Apply(event.message) {
				model.liveRunes += max(0, utf8.RuneCountInString(event.message.Text)-before)
				model.liveTokens = estimateTokensFromRunes(model.liveRunes)
				model.phase = phaseStreaming
				model.progress = "正在生成"
				model.stream = model.messages.Latest.Text
				model.liveCache = &streamViewCache{}
			}
		case "token":
			if model.messages.TurnID != "" {
				break
			}
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
			if model.messages.TurnID == "" {
				model.flushStream()
			}
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
	if event.err != nil && !cancelled {
		model.flushStream()
		model.enqueuePrint(model.styles.error.Render("本轮失败：" + event.err.Error()))
	} else if cancelled {
		model.flushStream()
		model.enqueuePrint(model.styles.dim.Render("已取消本轮任务"))
	} else {
		// The final snapshot is authoritative: streamed text can include prior
		// commentary, an incomplete draft or a superseded closeout answer.
		model.reply = event.result.Reply
		model.replyRendered = false
		if strings.TrimSpace(model.reply) == "" {
			model.reply = model.stream
		}
		if event.result.Outcome.Notice != "" {
			model.reply += "\n\n" + event.result.Outcome.Notice
		}
		model.stream = ""
		model.liveCache = &streamViewCache{}
	}
	if event.result.SessionID != "" {
		model.sessionID = event.result.SessionID
	}
	if event.result.Model != "" {
		model.runtime.Model = event.result.Model
		model.runtime.ModelDisplayName = event.result.ModelDisplayName
	} else {
		model.runtime = model.backend.RuntimeInfo(model.sessionID)
	}
	if event.result.Mode != "" {
		model.runtime.Mode = event.result.Mode
	}
	model.metrics = event.result.Metrics
	model.phase = phaseIdle
	model.reportActivity("idle")
	model.progress = ""
	if model.pendingClear {
		model.pendingClear = false
		disableMouse := model.discardToolGroup()
		model.clearSession()
		return []tea.Cmd{disableMouse, tea.ClearScreen}
	}
	if model.pendingQuit {
		return []tea.Cmd{model.commitTurn(), tea.Quit}
	}
	model.flushOverflowReply()
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
			Message:  func(event turn.Event) { send(turnEvent{kind: "message", message: event}) },
			Session:  func(id string) { send(turnEvent{kind: "session", text: id}) },
			Usage:    func(usage TokenUsage) { send(turnEvent{kind: "usage", usage: usage}) },
			Context:  func(usage turn.ContextUsage) { send(turnEvent{kind: "context", contextUsage: usage}) },
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
	model.liveCache = &streamViewCache{}
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

// Commit only the overflow. Keep the conclusion in the live frame so finishing
// a streamed answer cannot leave only tools and the input on screen. The history
// prefix and live suffix are disjoint; subsequent commits print only the suffix.
func (model *Model) flushOverflowReply() {
	if model.reply == "" {
		return
	}
	_, height, _ := model.liveBudgets()
	if len(model.tools.items) > 0 {
		// Reserve the same answer space when the tool drawer is expanded, so
		// toggling tools does not transfer more of the answer into history.
		height = min(height, max(1, (model.frameLimit()-len(model.footerLines())-len(model.auxiliaryLines()))/2))
	}
	lines := model.streamLines(model.contentWidth())
	if height <= 0 || len(lines) <= height {
		return
	}
	cut := len(lines) - height
	// Native history must contain the tools before any part of their answer.
	// Keep the data for Ctrl+O inspection, but do not leave a second tool record
	// between the answer's committed prefix and its live suffix.
	model.archiveToolGroup()
	model.enqueuePrint(strings.Join(lines[:cut], "\n"))
	model.reply = strings.Join(lines[cut:], "\n")
	model.replyRendered = true
	model.liveCache = &streamViewCache{}
}

func (model *Model) flushReply() {
	if model.reply == "" {
		return
	}
	model.enqueuePrint(strings.Join(model.streamLines(model.contentWidth()), "\n"))
	model.reply = ""
	model.replyRendered = false
	model.liveCache = &streamViewCache{}
}

// Insert at most one physical row at a time. A single Println longer than the
// terminal can push the old live frame into scrollback before clearing it,
// leaving duplicate drafts and tool rows behind. The live frame reserves one
// terminal row, so single-row insertions keep it inside the editable screen.
func printHistoryLines(text string, width int) tea.Cmd {
	lines := strings.Split(ansi.Hardwrap(text, width, false), "\n")
	commands := make([]tea.Cmd, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			line = " " // Bubble Tea ignores an empty Println message.
		}
		commands = append(commands, tea.Println(line))
	}
	return tea.Sequence(commands...)
}

func (model *Model) commitTurn() tea.Cmd {
	command := model.commitToolGroup()
	model.flushReply()
	return command
}

func (model *Model) commitToolGroup() tea.Cmd {
	if len(model.tools.items) == 0 {
		return nil
	}
	model.archiveToolGroup()
	return model.discardToolGroup()
}

func (model *Model) archiveToolGroup() {
	if len(model.tools.items) == 0 || model.tools.committed {
		return
	}
	model.enqueuePrint(ansi.Hardwrap(model.tools.render(model.styles, false), model.contentWidth(), false))
	model.tools.committed = true
	model.tools.expanded = false
	model.tools.offset = 0
	model.tools.cache = nil
	model.toolMouse = false
}

func (model *Model) discardToolGroup() tea.Cmd {
	model.tools = toolGroup{}
	model.toolMouse = false
	return nil
}

func (model *Model) clearSession() {
	model.contextUsage = nil
	model.sessionID = ""
	model.reportActivity("idle")
	model.runtime = model.backend.RuntimeInfo("")
	model.metrics = nil
	model.usage = nil
	model.liveTokens = 0
	model.liveRunes = 0
	model.queued = nil
	model.stream = ""
	model.reply = ""
	model.replyRendered = false
	model.liveCache = &streamViewCache{}
	model.tools = toolGroup{}
	model.toolMouse = false
	model.input.reset()
	model.closePanel()
	model.printQueue = nil
	model.statusNotice = "已清理显示；下一条消息将创建新会话"
}

func (model Model) reportActivity(state string) {
	if model.config.ActivitySink != nil {
		model.config.ActivitySink(state, model.sessionID)
	}
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
	parameterPicker := len(fields) > 0 && (strings.EqualFold(fields[0], "/model") || strings.EqualFold(fields[0], "/effort"))
	trailingSpace := strings.HasSuffix(rawValue, " ")
	suggestions := make([]suggestion, 0)
	seenSuggestions := make(map[string]struct{})
	appendSuggestion := func(item suggestion) {
		key := strings.ToLower(strings.TrimSpace(item.value))
		if key == "" {
			return
		}
		if _, exists := seenSuggestions[key]; exists {
			return
		}
		seenSuggestions[key] = struct{}{}
		suggestions = append(suggestions, item)
	}
	if len(fields) <= 1 && !trailingSpace && !parameterPicker {
		for _, command := range model.commands {
			if strings.HasPrefix(strings.ToLower(command.Name), strings.ToLower(value)) {
				appendSuggestion(suggestion{value: command.Name, description: command.Description})
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
				if strings.HasPrefix(strings.ToLower(argument.Value), strings.ToLower(prefix)) || (argument.Label != "" && strings.Contains(strings.ToLower(argument.Label), strings.ToLower(prefix))) {
					label := ""
					if argument.Label != "" {
						label = command.Name + " " + argument.Label
					}
					appendSuggestion(suggestion{value: command.Name + " " + argument.Value, label: label, description: argument.Description, confirm: parameterPicker})
				}
			}
		}
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
	// Keep the inline frame height stable while the completion panel is open.
	// Bubble Tea's Windows inline renderer otherwise resizes the frame for every
	// narrower match and can leave each previous panel in terminal scrollback.
	lines := make([]string, 0, maxSuggestions+1)
	title := "命令与参数"
	if len(model.suggestions) > 0 && model.suggestions[0].confirm {
		title = "选择模型"
		if strings.HasPrefix(model.suggestions[0].value, "/effort ") {
			title = "选择推理努力程度"
		}
		title += " · ↑/↓ 选择 · Enter 确认 · Tab 补全"
	} else if len(model.suggestions) > maxSuggestions {
		title += " · ↑/↓ 滚动选择"
	}
	if len(model.suggestions) > maxSuggestions {
		title += fmt.Sprintf(" · %d/%d", model.suggested+1, len(model.suggestions))
	}
	lines = append(lines, model.styles.accent.Render(title))
	start := max(0, model.suggested-maxSuggestions+1)
	end := min(len(model.suggestions), start+maxSuggestions)
	for index := start; index < end; index++ {
		item := model.suggestions[index]
		pointer := "  "
		if index == model.suggested {
			pointer = "› "
		}
		label := item.label
		if label == "" {
			label = item.value
		}
		line := pointer + label
		if item.description != "" && width > 40 {
			line += model.styles.dim.Render("  " + item.description)
		}
		lines = append(lines, line)
	}
	for len(lines) < maxSuggestions+1 {
		lines = append(lines, "")
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
	left = modelcatalog.QualifiedName(left, model.runtime.Model, model.runtime.ModelDisplayName)
	left += " · " + displayMode(model.runtime.Mode)
	right := model.statusNotice
	if model.phase != phaseIdle {
		elapsed := time.Since(model.startedAt).Round(100 * time.Millisecond)
		right = fmt.Sprintf("%s · %s %s · %s", model.tokenUsageStatus(), model.spinner.View(), model.phaseLabel(), elapsed)
	} else if model.metrics != nil || model.usage != nil {
		parts := []string{model.tokenUsageStatus()}
		speedLabel := "TPS —"
		if model.metrics != nil {
			if speed := model.metrics.TokensPerSecond; speed > 0 && !math.IsNaN(speed) && !math.IsInf(speed, 0) {
				// Reliability is checked against stream samples by the agent.
				// A high rate alone is not evidence of buffered delivery.
				speedLabel = fmt.Sprintf("%.1f tok/s", speed)
			}
		}
		parts = append(parts, speedLabel)
		if model.metrics != nil {
			parts = append(parts, fmt.Sprintf("首字 %.2fs", float64(model.metrics.FirstTokenLatencyMS)/1000))
			if model.metrics.CacheMetrics != nil && model.metrics.CacheMetrics.Available && model.metrics.CacheMetrics.InputTokens > 0 {
				rate := 100 * model.metrics.CacheMetrics.HitRate
				rate = min(100, max(0, rate))
				parts = append(parts, fmt.Sprintf("缓存命中 %.0f%%", rate))
			} else if model.metrics.CacheUsageAvailable && model.metrics.PromptTokens > 0 {
				rate := 100 * float64(model.metrics.CachedPromptTokens) / float64(model.metrics.PromptTokens)
				rate = min(100, max(0, rate))
				parts = append(parts, fmt.Sprintf("缓存命中 %.0f%%", rate))
			}
		}
		right = strings.Join(parts, " · ")
	}
	if status := model.mcpStatusLabel(); status != "" {
		if right == "" {
			right = status
		} else {
			right = status + " · " + right
		}
	}
	if model.quitConfirmationActive() {
		right = quitConfirmHint
	}
	if right == "" {
		right = "Enter 发送 · Alt+Enter 换行 · / 命令"
	}
	if usage := model.contextUsage; usage != nil && usage.Limit > 0 && !model.quitConfirmationActive() {
		// Keep the context indicator visible even on narrow terminals. Unlike
		// in/out billing totals, each backend snapshot replaces the previous one.
		right = fmt.Sprintf("%.0f%%/%s · %s", 100*float64(max(0, usage.Tokens))/float64(usage.Limit), formatTokenCount(usage.Limit), right)
	}
	left, right = right, left
	left = ansi.Truncate(left, width, "…")
	right = ansi.Truncate(right, width, "…")
	space := width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		return model.styles.dim.Render(left + "\n" + right)
	}
	return model.styles.dim.Render(left + strings.Repeat(" ", space) + right)
}

func (model Model) tokenUsageStatus() string {
	if model.usage != nil {
		return "in " + formatTokenCount(model.usage.InputTokens) + " · out " + formatTokenCount(model.usage.OutputTokens)
	}
	if model.metrics != nil {
		return "in " + formatTokenCount(model.metrics.PromptTokens) + " · out " + formatTokenCount(model.metrics.CompletionTokens)
	}
	if model.liveRunes > 0 {
		return "in — · out " + formatTokenCount(model.liveTokens)
	}
	return "in — · out —"
}

func formatTokenCount(tokens int) string {
	tokens = max(0, tokens)
	digits := fmt.Sprint(tokens)
	var value float64
	var unit string
	switch {
	case tokens >= 999_950: // Promote rounded 1000K to 1M.
		value, unit = float64(tokens)/1_000_000, "M"
	case tokens >= 1_000:
		value, unit = float64(tokens)/1_000, "K"
	}
	if unit != "" {
		digits = strings.TrimSuffix(fmt.Sprintf("%.1f", value), ".0") + unit
	}
	return digits
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
	if model.provider != nil {
		model.provider.resize(model.contentWidth())
	}
}

func (model Model) contentWidth() int { return max(1, model.width-2) }

func (model Model) startupLines() []string {
	mode := displayMode(model.runtime.Mode)
	notice := "Run 模式可修改文件或运行命令，请留意工具摘要与工作区边界。"
	if mode == "Ask" {
		notice = "Ask 模式仅用于只读问答，不会修改文件或执行命令。"
	}
	return []string{
		model.styles.accent.Render("bqAgent") + "  Harness",
		model.styles.dim.Render(notice),
		fmt.Sprintf("工作区  %s", model.config.Workspace),
	}
}

func historyToolGroup(tools []ToolEvent) toolGroup {
	group := toolGroup{}
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" && strings.TrimSpace(tool.Preview) == "" {
			continue
		}
		group.add(ToolEvent{Kind: "tool_start", ID: tool.ID, Name: tool.Name, Arguments: tool.Arguments})
		group.add(ToolEvent{
			Kind: "tool_result", ID: tool.ID, Name: tool.Name, Status: tool.Status,
			Arguments: tool.Arguments, Preview: tool.Preview, Truncated: tool.Truncated,
		})
	}
	return group
}

func (model Model) renderHistory(history History) []string {
	lines := []string{model.styles.dim.Render("恢复会话 " + history.ID)}
	if history.Omitted > 0 {
		lines = append(lines, model.styles.dim.Render(fmt.Sprintf("已省略更早的 %d 条消息（恢复显示上限 200 KiB）", history.Omitted)))
	}
	for _, message := range history.Messages {
		if message.Role == "user" {
			lines = append(lines, model.renderTurnSeparator(), model.renderUserMessage(message.Content))
			continue
		}
		if rendered := historyToolGroup(message.Tools).render(model.styles, false); rendered != "" {
			lines = append(lines, rendered)
		}
		if strings.TrimSpace(message.Content) != "" {
			lines = append(lines, renderMarkdown(message.Content, model.contentWidth(), model.config.NoColor))
		}
	}
	return lines
}

func (model Model) helpText() string {
	return `快捷键
  Enter 发送    Alt+Enter 换行    Tab/Shift+Tab 补全
  ↑/↓ 历史      Ctrl+C 复制选中文本    Ctrl+V 粘贴
  连按两次 Ctrl+C（2 秒内）或 Ctrl+Q 安全退出
  Esc 打断当前回复；空闲时关闭面板/收起工具详情
  Ctrl+L 清理当前视口和草稿（保留 Session）
  Ctrl+O 展开/收起已合并的工具详情（也可点击工具汇总行）
  回答完整展示，使用终端滚动查看；PgUp/PgDn 仅翻页查看工具详情

本地命令
  /help 帮助    /provider 配置模型 Provider    /clear 新建会话并清理显示    /exit 安全退出
  /effort [auto|low|medium|high|max] 查看或设置推理努力程度

后端命令
  /run（完整工具）  /ask（只读问答）  /model  /fast  /skill  /memory  /feedback  /agent
  /claude  /codex  /cursor  /opencode  /pi  /default  /stop`
}

func mergeCommands(dynamic []Command) []Command {
	commands := chatcommand.Builtins()
	byName := make(map[string]int, len(commands))
	for index, command := range commands {
		byName[normalizedCommandName(command.Name)] = index
	}
	for _, command := range dynamic {
		key := normalizedCommandName(command.Name)
		if key == "" {
			continue
		}
		if index, ok := byName[key]; ok {
			command.Name = commands[index].Name
			commands[index] = command
			continue
		}
		command.Name = strings.TrimSpace(command.Name)
		byName[key] = len(commands)
		commands = append(commands, command)
	}
	return commands
}

func normalizedCommandName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func displayMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "ask") {
		return "Ask"
	}
	return "Run"
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

// MCP status is optional to keep alternate backends independent of discovery.
func (model *Model) refreshMCPStatus() {
	if backend, ok := model.backend.(interface{ MCPStatus() []mcp.ServerStatus }); ok {
		model.mcpStatus = backend.MCPStatus()
	}
}

func (model Model) mcpStatusLabel() string {
	var connecting, available, failed int
	for _, status := range model.mcpStatus {
		switch status.State {
		case "connecting", "unverified":
			connecting++
		case "available":
			available++
		case "error":
			failed++
		}
	}
	var parts []string
	if connecting > 0 {
		parts = append(parts, fmt.Sprintf("连接中 %d", connecting))
	}
	if available > 0 {
		parts = append(parts, fmt.Sprintf("已连接 %d", available))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d", failed))
	}
	if len(parts) == 0 {
		return ""
	}
	return "MCP " + strings.Join(parts, " · ")
}
