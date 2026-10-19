package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
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
	// inputs is nil when the backend cannot consume mid-turn input.
	inputs *turn.InputQueue
	// pending mirrors accepted but not yet applied inputs, in acceptance order.
	pending []pendingInput
}

type pendingInput struct {
	id   string
	kind turn.InputKind
	text string
}

func supportsTurnInput(backend Backend) bool {
	capable, ok := backend.(TurnInputBackend)
	return ok && capable.SupportsTurnInput()
}

type queuedPrompt struct {
	value  string
	images []Image
}

type suggestion struct {
	label       string
	value       string
	description string
	confirm     bool
	// file marks an @ mention candidate; value is its workspace path.
	file bool
}

const maxSuggestions = 8

const quitConfirmWindow = 2 * time.Second
const quitConfirmHint = "再次按 Ctrl+C 退出（2 秒内）"

type Model struct {
	messages turn.Messages
	// sealedItems are commentary messages already printed to history.
	sealedItems map[string]bool
	task        taskProgress
	backend     Backend
	config      Config
	styles      theme
	input       promptInput
	spinner     spinner.Model
	width       int
	height      int

	phase            phase
	progress         string
	startedAt        time.Time
	turnElapsed      time.Duration // frozen duration of the last turn run in this process; zero when unknown
	stream           string
	reply            string
	replyRendered    bool
	active           *activeTurn
	sequence         uint64
	inputSequence    uint64
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

	commands         []Command
	suggestions      []suggestion
	suggested        int
	panelOpen        bool
	mention          *mentionQuery
	mentionDismissed int
	fileIndex        workspaceFileIndex
	tools            toolGroup
	toolMouse        bool
	toolInspector    bool
	inspectorPending bool
	inspection       toolInspection
	liveCache        *streamViewCache
	provider         *providerWizard

	startup       []string
	initialTask   string
	initialSent   bool
	printQueue    []string
	transcript    []string
	historyPrints int
	historyQueue  []historyPrint
	statusNotice  string
	// inputNotice reports prompt-editing results such as image attachments.
	// It outranks the status line until the next key press.
	inputNotice     string
	clipboard       clipboardReader
	mcpStatus       []mcp.ServerStatus
	mcpStatusUntil  time.Time
	mcpStatusHidden bool
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
		backend:          backend,
		config:           config,
		styles:           newTheme(config.NoColor),
		input:            newPromptInput(config.NoColor),
		width:            82,
		phase:            phaseIdle,
		sessionID:        strings.TrimSpace(config.SessionID),
		initialTask:      strings.TrimSpace(config.InitialTask),
		historyStore:     newHistoryStore(config.AgentDir, config.Workspace),
		liveCache:        &streamViewCache{},
		clipboard:        systemClipboardReader(),
		mentionDismissed: -1,
	}
	model.spinner = spinner.New(spinner.WithSpinner(spinner.Dot))
	if !config.NoColor {
		model.spinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(accentColor))
	}
	model.refreshMCPStatus(time.Now())
	model.runtime = backend.RuntimeInfo(model.sessionID)
	model.commands = mergeCommands(backend.Commands())
	model.history, _ = model.historyStore.load()
	model.historyIndex = len(model.history)
	model.startup = model.startupLines()
	if model.sessionID != "" {
		if history, err := backend.History(model.sessionID, ResumeHistoryBudget); err == nil {
			if history.Task != nil && history.Task.Status != "completed" && history.Task.Status != "cancelled" {
				model.task.state = history.Task
			}
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
		commands = append(commands, model.handleMouse(message)...)
		model.quitConfirmUntil = time.Time{}
	case tea.MouseWheelMsg:
		if model.toolInspector {
			model.quitConfirmUntil = time.Time{}
			switch message.Button {
			case tea.MouseWheelUp:
				model.handleToolInspectorKey("up")
			case tea.MouseWheelDown:
				model.handleToolInspectorKey("down")
			}
		} else if model.tools.expanded && model.tools.liveVisible() {
			height := max(1, model.toolViewportHeight()-1)
			lines := model.tools.viewLines(model.styles, model.contentWidth())
			limit := max(0, len(lines)-1-height)
			model.tools.offset = min(model.tools.offset, limit)
			switch message.Button {
			case tea.MouseWheelUp:
				model.tools.offset = max(0, model.tools.offset-3)
			case tea.MouseWheelDown:
				model.tools.offset = min(limit, model.tools.offset+3)
			}
		} else if model.provider != nil {
			commands = append(commands, model.provider.updateInput(message))
		} else {
			commands = append(commands, model.input.update(message))
		}
	case turnEvent:
		commands = append(commands, model.handleTurnEvent(message)...)
	case providerSettingsMsg:
		commands = append(commands, model.handleProviderSettings(message)...)
	case providerModelsMsg:
		commands = append(commands, model.handleProviderModels(message)...)
	case providerSavedMsg:
		commands = append(commands, model.handleProviderSaved(message)...)
	case imageAttachMsg:
		commands = append(commands, model.handleImageAttach(message)...)
	case fileIndexMsg:
		model.fileIndex.files = message.files
		model.fileIndex.loadedAt = time.Now()
		model.fileIndex.loading = false
		if model.mention != nil {
			model.refreshSuggestions()
		}
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
		model.refreshMCPStatus(time.Time(message))
		if !time.Time(message).Before(model.quitConfirmUntil) {
			model.quitConfirmUntil = time.Time{}
		}
		commands = append(commands, tickCommand())
	default:
		if model.toolInspector {
			break // The completed-turn inspector must not edit the hidden prompt.
		}
		if _, pasted := message.(tea.PasteMsg); pasted {
			model.inspectorPending = false
			model.quitConfirmUntil = time.Time{}
		}
		if model.provider != nil {
			commands = append(commands, model.provider.updateInput(message))
			break
		}
		if paste, pasted := message.(tea.PasteMsg); pasted {
			if paths := pastedImagePaths(paste.Content); len(paths) > 0 {
				commands = append(commands, loadPastedImages(paths, paste.Content))
				break
			}
		}
		// Textarea clipboard reads complete asynchronously. Forward messages
		// unknown to the model so Ctrl+V and bracketed paste reach the input.
		command := model.input.update(message)
		commands = append(commands, command)
		model.refreshSuggestions()
		model.resizeInput()
	}
	if model.toolInspector {
		model.normalizeToolInspection()
	}
	model.flushOverflowReply()
	if text := model.flushTranscript(); text != "" {
		model.historyQueue = append(model.historyQueue, historyPrint{
			text: text, after: tea.Batch(commands...),
		})
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
	if model.inspectorPending && model.toolHistoryReady() {
		model.inspectorPending = false
		model.toolInspector = true
	}
	if model.fileIndex.wanted && !model.fileIndex.loading && model.config.Workspace != "" {
		model.fileIndex.loading = true
		commands = append(commands, indexWorkspaceFiles(model.config.Workspace))
	}
	model.fileIndex.wanted = false
	return model, tea.Batch(commands...)
}

func (model Model) View() tea.View {
	if model.toolInspector && model.toolHistoryReady() {
		return model.toolInspectorView()
	}
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
	// String prefers associated text; use the physical combination for queue keys.
	switch stroke := key.Keystroke(); stroke {
	case "alt+q", "ctrl+s":
		name = stroke
	}
	if name == "ctrl+c" {
		if model.quitConfirmationActive() {
			return model.requestQuit()
		}
		model.quitConfirmUntil = time.Now().Add(quitConfirmWindow)
	} else {
		model.quitConfirmUntil = time.Time{}
	}
	model.inputNotice = ""
	if model.provider != nil {
		return model.handleProviderKey(key)
	}
	if model.inspectorPending && name != "ctrl+o" {
		model.inspectorPending = false
	}
	if model.toolInspector {
		return model.handleToolInspectorKey(name)
	}
	switch name {
	case "ctrl+c":
		model.input.reset()
		model.closePanel()
		model.resizeInput()
		return nil
	case "ctrl+v", "alt+v":
		// Windows Terminal keeps Ctrl+V for its own paste; Alt+V reaches us.
		return []tea.Cmd{pasteClipboard(model.clipboard)}
	case "ctrl+o":
		model.toggleToolGroup()
		return nil
	case "ctrl+q":
		return model.requestQuit()
	case "ctrl+l":
		disableMouse := model.discardToolGroup()
		model.transcript = nil
		model.reply = ""
		model.replyRendered = false
		model.input.reset()
		model.closePanel()
		model.resizeInput()
		return []tea.Cmd{disableMouse, tea.ClearScreen}
	case "esc":
		if model.mention != nil && model.panelOpen {
			model.mentionDismissed = model.mention.start
			model.closePanel()
			return nil
		}
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
	case "enter", "alt+q", "ctrl+s":
		kind := turn.Steering
		if name != "enter" {
			if model.active == nil {
				model.inputNotice = "当前没有运行中的任务，按 Enter 发送"
				return nil
			}
			kind = turn.FollowUp
		}
		if model.mention != nil && model.panelOpen {
			if len(model.suggestions) > 0 {
				model.insertMention()
				return nil
			}
			model.closePanel()
		}
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
		images := model.input.images()
		if len(images) > 0 {
			if strings.HasPrefix(value, "/") {
				model.inputNotice = "Slash 命令不能附带图片"
				return nil
			}
			if err := validateImageSet(images); err != nil {
				model.inputNotice = err.Error()
				return nil
			}
		}
		model.input.reset()
		model.closePanel()
		model.resizeInput()
		if model.active != nil {
			before := model.statusNotice
			if model.steer(value, images, kind) {
				return nil
			}
			// steer explains why it declined; queue must not overwrite that.
			notice := model.statusNotice
			commands := model.queue(value, images)
			if notice != before {
				model.statusNotice = notice
			}
			return commands
		}
		return []tea.Cmd{model.submit(value, images...)}
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
	if model.toolInspector {
		model.handleToolInspectorMouse(event)
		return nil
	}
	if !model.tools.canExpand() || event.Button != tea.MouseLeft || model.toolViewportHeight() == 0 {
		return nil
	}
	viewLines, headerRow := model.liveLayout()
	if headerRow < 0 {
		return nil
	}
	headerY := max(0, model.height-len(viewLines)) + headerRow
	if model.tools.expanded && model.tools.detail == 0 && event.X >= 0 && event.X < model.contentWidth() {
		row := event.Y - headerY - 1
		bodyHeight := max(0, model.toolViewportHeight()-1)
		offset := min(model.tools.offset, max(0, len(model.tools.items)-bodyHeight))
		index := offset + row
		if row >= 0 && row < bodyHeight && index < len(model.tools.items) {
			model.tools.detail = index + 1
			model.tools.offset = 0
			return nil
		}
	}
	if event.Y != headerY && !(model.height == 0 && event.Y == 0) {
		return nil
	}
	if model.tools.expanded && model.tools.detail > 0 {
		model.tools.detail = 0
		model.tools.offset = 0
		return nil
	}
	model.toggleToolGroup()
	return nil
}

func (model *Model) toggleToolGroup() {
	if len(model.tools.items) > 0 && model.active == nil && (model.sequence > 0 || model.reply != "" || model.tools.committed) {
		if model.inspectorPending {
			model.inspectorPending = false
			return
		}
		model.inspection = toolInspection{cache: &toolDetailCache{}}
		model.inspectorPending = !model.toolHistoryReady()
		model.toolInspector = !model.inspectorPending
		return
	}
	if model.tools.canExpand() {
		model.tools.expanded = !model.tools.expanded
		model.tools.detail = 0
		model.tools.offset = 0
		if model.tools.committed {
			model.toolMouse = model.tools.expanded
		}
	}
}

func (model *Model) submit(value string, images ...Image) tea.Cmd {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if _, ok := model.backend.(AttachmentBackend); len(images) > 0 && !ok {
		model.input.restoreDraft(value, images)
		model.resizeInput()
		model.inputNotice = "当前后端不支持图片输入"
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
	attachments := TurnAttachments{Images: images}
	if _, ok := model.backend.(AttachmentBackend); ok && !strings.HasPrefix(value, "/") {
		attachments.Files = mentionedFiles(model.config.Workspace, value)
	}
	model.recordHistory(stripImageLabels(value, images))
	model.enqueueUserTurn(value)
	model.sequence++
	model.phase = phaseThinking
	model.messages = turn.Messages{}
	model.sealedItems = map[string]bool{}
	// A new turn owns its own task snapshots: the agent re-sends an unfinished
	// task at turn start, so the previous projection (and its stale mark) is
	// dropped instead of lingering beside the new turn.
	model.task = taskProgress{}
	model.reportActivity("working")
	model.progress = "正在思考"
	model.startedAt = time.Now()
	model.turnElapsed = 0
	model.metrics = nil
	model.usage = nil
	model.liveTokens = 0
	model.liveRunes = 0
	model.statusNotice = ""
	turnContext, cancel := context.WithCancel(model.config.Context)
	events := make(chan turnEvent, 256)
	active := &activeTurn{sequence: model.sequence, cancel: cancel, events: events}
	if supportsTurnInput(model.backend) && !strings.HasPrefix(value, "/") {
		active.inputs = &turn.InputQueue{}
		turnContext = turn.WithInputQueue(turnContext, active.inputs)
	}
	model.active = active
	return tea.Batch(toolCommand, model.spinner.Tick, launchTurn(model.backend, turnContext, model.sequence, model.sessionID, value, attachments, events))
}

// steer hands plain text to the running turn. It reports false when the input
// must use the legacy next-turn queue (slash commands, attachments, backends
// without mid-turn input, or a turn that is already finishing).
func (model *Model) steer(value string, images []Image, kind turn.InputKind) bool {
	active := model.active
	if active == nil || active.inputs == nil || strings.HasPrefix(strings.TrimSpace(value), "/") {
		return false
	}
	if len(images) > 0 {
		model.statusNotice = "图片暂不支持运行中插入，已排到下一轮"
		return false
	}
	if _, ok := model.backend.(AttachmentBackend); ok && len(mentionedFiles(model.config.Workspace, value)) > 0 {
		model.statusNotice = "@文件暂不支持运行中插入，已排到下一轮"
		return false
	}
	model.inputSequence++
	id := fmt.Sprintf("input-%d-%d", active.sequence, model.inputSequence)
	if err := active.inputs.Add(turn.Input{ID: id, Kind: kind, Text: value}); err != nil {
		model.statusNotice = "本轮即将结束，已排到下一轮"
		return false
	}
	active.pending = append(active.pending, pendingInput{id: id, kind: kind, text: value})
	if kind == turn.FollowUp {
		model.statusNotice = "已加入跟进队列"
	} else {
		model.statusNotice = "已发送引导，将在下次模型调用前生效"
	}
	return true
}

// presentInput prints a consumed input in conversation order: earlier tools
// and streamed text first, then the user's message under a new turn marker.
// Later tools start a fresh group below it.
func (model *Model) presentInput(input pendingInput) {
	model.commitToolGroup()
	model.flushStream()
	model.recordHistory(input.text)
	model.enqueueUserTurn(input.text)
}

// applyInput shows an input the agent has consumed and removes it from the
// pending list. Output order follows the model's view of the conversation.
func (model *Model) applyInput(event turn.Event) {
	active := model.active
	for i, input := range active.pending {
		if input.id != event.ItemID {
			continue
		}
		active.pending = append(active.pending[:i], active.pending[i+1:]...)
		model.presentInput(input)
		return
	}
}

func (model *Model) queue(value string, images []Image) []tea.Cmd {
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
		model.queued = &queuedPrompt{value: value, images: images}
		model.statusNotice = "消息已排队"
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(value), "/") || strings.HasPrefix(strings.TrimSpace(model.queued.value), "/") {
		model.input.restoreDraft(value, images)
		model.statusNotice = "Slash 命令需单独排队"
		return nil
	}
	merged := append(append([]Image(nil), model.queued.images...), images...)
	if err := validateImageSet(merged); err != nil {
		model.input.restoreDraft(value, images)
		model.resizeInput()
		model.inputNotice = "无法合并到排队消息：" + err.Error()
		return nil
	}
	model.queued.value += "\n" + value
	model.queued.images = merged
	return nil
}

func (model *Model) handleTurnEvent(event turnEvent) []tea.Cmd {
	if event.sequence != model.sequence || model.active == nil || model.active.sequence != event.sequence {
		return nil
	}
	if event.kind != "done" {
		commands := []tea.Cmd{waitTurnEvent(model.active.events)}
		if event.kind == "message" && event.message.Kind == "task.updated" {
			model.task.apply(event.message)
			return commands
		}
		if event.kind == "message" && event.message.Kind == "input.applied" {
			// Applied input is a user message, not part of the assistant projection.
			model.applyInput(event.message)
			return commands
		}
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
			if model.sealedItems[event.message.ItemID] {
				break // Already committed to history before its tools ran.
			}
			before := utf8.RuneCountInString(model.messages.Items[event.message.ItemID].Text)
			previous := model.messages.Latest.ItemID
			if model.messages.Apply(event.message) {
				if previous != "" && previous != model.messages.Latest.ItemID && model.stream != "" {
					// A new message replaces the displayed one; commit the old text
					// (after the tools that preceded it) instead of overwriting it.
					model.sealItem(previous)
					model.commitToolGroup()
					model.flushStream()
				}
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
			// Commentary and tool calls alternate: print the previous tool group,
			// then the commentary that followed it, so neither replaces the other.
			if model.stream != "" {
				model.commitToolGroup()
				if model.messages.TurnID != "" {
					model.sealItem(model.messages.Latest.ItemID)
				}
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
	var recovered []string
	if model.active.inputs != nil {
		unconsumed := map[string]bool{}
		for _, input := range model.active.inputs.Close() {
			recovered = append(recovered, input.Text)
			unconsumed[input.ID] = true
		}
		// The agent consumed everything else, but a cancelled turn may have
		// dropped the input.applied event; show those so nothing disappears.
		for _, input := range model.active.pending {
			if !unconsumed[input.id] {
				model.presentInput(input)
			}
		}
		model.active.pending = nil
	}
	if !model.startedAt.IsZero() {
		model.turnElapsed = max(0, time.Since(model.startedAt))
	}
	model.active = nil
	// Release native scrollback even when a short turn leaves tools unarchived.
	model.toolMouse = false
	model.tools.expanded = false
	if event.err != nil && !cancelled {
		model.flushStream()
		model.enqueuePrint(turnFailureSummary(event.err.Error(), model.styles, model.contentWidth()))
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
	if model.queued != nil || len(recovered) > 0 {
		var queued queuedPrompt
		if model.queued != nil {
			queued = *model.queued
		}
		model.queued = nil
		if cancelled || len(recovered) > 0 {
			// Unconsumed mid-turn input comes first, one message per line.
			parts := append([]string(nil), recovered...)
			if queued.value != "" {
				parts = append(parts, queued.value)
			}
			if draft := strings.TrimSpace(model.input.value()); draft != "" {
				parts = append(parts, draft)
				queued.images = append(queued.images, model.input.images()...)
			}
			model.input.restoreDraft(strings.Join(parts, "\n"), queued.images)
			model.resizeInput()
			model.statusNotice = "已将排队内容恢复到输入框"
			return nil
		}
		return []tea.Cmd{model.submit(queued.value, queued.images...)}
	}
	return nil
}

func launchTurn(backend Backend, ctx context.Context, sequence uint64, sessionID, value string, attachments TurnAttachments, events chan turnEvent) tea.Cmd {
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
			var result TurnResult
			var err error
			if attachmentBackend, ok := backend.(AttachmentBackend); ok && !attachments.empty() {
				result, err = attachmentBackend.RunTurnWithAttachments(ctx, sessionID, value, attachments, sink)
			} else {
				result, err = backend.RunTurn(ctx, sessionID, value, sink)
			}
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

func (model *Model) sealItem(id string) {
	if model.sealedItems == nil {
		model.sealedItems = map[string]bool{}
	}
	model.sealedItems[id] = true
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
	return model.styles.user.Render("➤") + " " + strings.ReplaceAll(value, "\n", "\n  ") + "\n"
}

func (model Model) renderTurnSeparator() string {
	width := model.contentWidth()
	heading := ansi.Truncate("── ❯❯ ", width, "")
	line := heading + strings.Repeat("─", max(0, width-ansi.StringWidth(heading)))
	// Keep spacing attached to the heading so enqueuePrint preserves it.
	return "\n\n" + model.styles.accent.Render(line) + "\n"
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
	if model.reply == "" || model.toolInspector {
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
	model.enqueuePrint(ansi.Hardwrap(model.tools.render(model.styles, false, model.contentWidth()), model.contentWidth(), false))
	model.tools.committed = true
	model.tools.expanded = false
	model.tools.cache = nil
	model.toolMouse = false
}

func (model *Model) discardToolGroup() tea.Cmd {
	model.toolInspector = false
	model.inspectorPending = false
	model.inspection = toolInspection{}
	model.tools = toolGroup{}
	model.toolMouse = false
	return nil
}

func (model *Model) clearSession() {
	model.task = taskProgress{}
	model.toolInspector = false
	model.inspectorPending = false
	model.inspection = toolInspection{}
	model.contextUsage = nil
	model.sessionID = ""
	model.reportActivity("idle")
	model.runtime = model.backend.RuntimeInfo("")
	model.metrics = nil
	model.usage = nil
	model.turnElapsed = 0
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
	model.transcript = nil
	model.statusNotice = "已清理显示；下一条消息将创建新会话"
}

func (model Model) reportActivity(state string) {
	if model.config.ActivitySink != nil {
		model.config.ActivitySink(state, model.sessionID)
	}
}

// handleImageAttach links prepared images into the prompt. When nothing could
// be attached, the original text is pasted so Ctrl+V still works for text.
func (model *Model) handleImageAttach(message imageAttachMsg) []tea.Cmd {
	if model.toolInspector || model.provider != nil {
		return nil
	}
	failure := message.err
	var labels []string
	size := 0
	for _, image := range message.images {
		if err := model.input.attachImage(image); err != nil {
			failure = err
			break
		}
		labels = append(labels, fmt.Sprintf("[图片 #%d]", model.input.nextImage))
		size += len(image.Data)
	}
	var commands []tea.Cmd
	if len(labels) == 0 && message.text != "" {
		commands = append(commands, model.input.update(tea.PasteMsg{Content: message.text}))
	}
	switch {
	case failure != nil:
		model.inputNotice = "图片未添加：" + failure.Error()
	case len(labels) > 0:
		model.inputNotice = fmt.Sprintf("已添加 %s · %s", strings.Join(labels, " "), formatImageBytes(size))
	}
	model.refreshSuggestions()
	model.resizeInput()
	return commands
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
	if model.refreshMentionSuggestions() {
		return
	}
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
	if model.mention != nil && model.panelOpen && len(model.suggestions) > 0 {
		model.insertMention()
		return
	}
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
	model.mention = nil
}

// refreshMentionSuggestions opens the workspace file picker while the cursor
// is inside an @token. It reports whether the token owns the panel.
func (model *Model) refreshMentionSuggestions() bool {
	mention, ok := activeMention([]rune(model.input.displayValue()), model.input.cursorOffset())
	if !ok || model.config.Workspace == "" {
		model.mentionDismissed = -1
		model.mention = nil
		return false
	}
	if mention.start == model.mentionDismissed {
		model.closePanel()
		return true
	}
	if model.mention == nil || model.mention.start != mention.start {
		model.suggested = 0
		if time.Since(model.fileIndex.loadedAt) > fileIndexTTL {
			model.fileIndex.wanted = true
		}
	}
	if model.mention != nil && model.mention.query != mention.query {
		model.suggested = 0
	}
	model.mention = &mention
	files := searchFiles(model.fileIndex.files, mention.query, 200)
	model.suggestions = make([]suggestion, 0, len(files))
	for _, file := range files {
		directory := path.Dir(file)
		if directory == "." {
			directory = "./"
		} else {
			directory += "/"
		}
		model.suggestions = append(model.suggestions, suggestion{value: file, label: path.Base(file), description: directory, file: true})
	}
	model.panelOpen = true
	if model.suggested >= len(model.suggestions) {
		model.suggested = 0
	}
	return true
}

// insertMention replaces the @token with the selected workspace path.
func (model *Model) insertMention() {
	mention := *model.mention
	selected := model.suggestions[model.suggested]
	value := []rune(model.input.displayValue())
	inserted := []rune(mentionText(selected.value))
	updated := append(append(append([]rune(nil), value[:mention.start]...), inserted...), value[min(mention.end, len(value)):]...)
	model.input.area.SetValue(string(updated))
	model.input.setCursorOffset(mention.start + len(inserted))
	model.input.removeOrphanChips()
	model.closePanel()
	model.resizeInput()
}

func (model Model) renderSuggestions(width int) string {
	// Keep the inline frame height stable while the completion panel is open.
	// Bubble Tea's Windows inline renderer otherwise resizes the frame for every
	// narrower match and can leave each previous panel in terminal scrollback.
	if model.mention != nil {
		return model.renderFileSuggestions(width)
	}
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

func (model Model) renderFileSuggestions(width int) string {
	lines := make([]string, 0, maxSuggestions+1)
	title := "工作区文件 · ↑/↓ 选择 · Enter/Tab 插入 · Esc 关闭"
	if len(model.suggestions) > maxSuggestions {
		title += fmt.Sprintf(" · %d/%d", model.suggested+1, len(model.suggestions))
	}
	lines = append(lines, model.styles.accent.Render(title))
	switch {
	case len(model.suggestions) == 0 && (model.fileIndex.loading || model.fileIndex.loadedAt.IsZero()):
		lines = append(lines, model.styles.dim.Render("  正在索引工作区文件…"))
	case len(model.suggestions) == 0:
		lines = append(lines, model.styles.dim.Render("  没有匹配的文件"))
	}
	start := max(0, model.suggested-maxSuggestions+1)
	end := min(len(model.suggestions), start+maxSuggestions)
	nameWidth := 0
	for _, item := range model.suggestions[start:end] {
		nameWidth = max(nameWidth, ansi.StringWidth(item.label))
	}
	nameWidth = min(nameWidth, max(12, width/3))
	for index := start; index < end; index++ {
		item := model.suggestions[index]
		pointer := "  "
		if index == model.suggested {
			pointer = "› "
		}
		name := ansi.Truncate(item.label, nameWidth, "…")
		line := pointer + name + strings.Repeat(" ", nameWidth-ansi.StringWidth(name)+2) + model.styles.dim.Render(item.description)
		if index == model.suggested {
			line = pointer + model.styles.accent.Render(name) + strings.Repeat(" ", nameWidth-ansi.StringWidth(name)+2) + model.styles.dim.Render(item.description)
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
	if model.inputNotice != "" {
		right = model.inputNotice
	} else if model.phase != phaseIdle {
		elapsed := formatTurnElapsed(time.Since(model.startedAt))
		right = fmt.Sprintf("%s · %s %s · 耗时 %s", model.tokenUsageStatus(), model.spinner.View(), model.phaseLabel(), elapsed)
	} else if model.metrics != nil || model.usage != nil || model.turnElapsed > 0 {
		parts := []string{}
		if model.metrics != nil || model.usage != nil {
			parts = append(parts, model.tokenUsageStatus())
		}
		if model.turnElapsed > 0 {
			parts = append(parts, "耗时 "+formatTurnElapsed(model.turnElapsed))
		}
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
		if model.active != nil && model.active.inputs != nil {
			right = "Enter 引导 · Alt+Q/Ctrl+S 跟进 · Alt+Enter 换行"
		}
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

// formatTurnElapsed keeps sub-minute runs at the live 100ms precision and
// rounds longer ones to whole seconds, e.g. 1m24s.
func formatTurnElapsed(elapsed time.Duration) string {
	if elapsed >= time.Minute {
		return elapsed.Round(time.Second).String()
	}
	return elapsed.Round(100 * time.Millisecond).String()
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
		if rendered := historyToolGroup(message.Tools).render(model.styles, false, model.contentWidth()); rendered != "" {
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
  生成中 Enter 引导当前任务（下次模型调用前生效），Alt+Q / Ctrl+S 排为跟进（当前工作自然结束后处理）
  若终端把 Option+Q 输入为字符，请用 Ctrl+S；空闲时按 Enter 发送
  生成中的 Slash 命令、图片和 @文件 仍排到下一轮
  @ 搜索并引用工作区文件，发送时附带文件内容
  ↑/↓ 历史      Ctrl+C 清空输入框
  Ctrl+V / Alt+V 粘贴；剪贴板有图片或粘贴图片路径时作为 [图片 #N] 附件发送
  连按两次 Ctrl+C（2 秒内）或 Ctrl+Q 安全退出
  Esc 打断当前回复；空闲时关闭面板/收起工具详情
  Ctrl+L 清理当前视口和草稿（保留 Session）
  Ctrl+O 展开/收起工具；回合完成后打开工具列表
  工具列表中 ↑/↓ 选择，Enter 或点击查看单项详情
  单项详情中方向键/滚轮滚动，Home/End 跳到首尾；Esc 返回列表
  工具错误默认最多预览 3 行；详情显示已收到的参数和输出
  回答完整展示，使用终端滚动查看

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
	if strings.Contains(value, "暂无有效进展") {
		return "距上次有效进展" + stallWaitText(value)
	}
	if strings.Contains(value, "模型已恢复有效进展") {
		return "模型已恢复响应"
	}
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

// stallWaitText extracts the "已 Ns" wait duration from a stall progress line.
func stallWaitText(value string) string {
	value = strings.TrimSpace(value)
	if start := strings.Index(value, "距上次有效进展"); start >= 0 {
		value = value[start+len("距上次有效进展"):]
	}
	if start := strings.Index(value, "已 "); start >= 0 {
		rest := value[start:]
		if end := strings.IndexAny(rest, "，,"); end >= 0 {
			rest = rest[:end]
		}
		return strings.TrimSpace(rest) + "，暂无有效进展"
	}
	return "暂无有效进展"
}

// MCP status is optional to keep alternate backends independent of discovery.
func (model *Model) refreshMCPStatus(now time.Time) {
	if model.mcpStatusHidden {
		return
	}
	if backend, ok := model.backend.(interface{ MCPStatus() []mcp.ServerStatus }); ok {
		statuses := backend.MCPStatus()
		changed := !slices.EqualFunc(model.mcpStatus, statuses, func(a, b mcp.ServerStatus) bool {
			return a.Name == b.Name && a.State == b.State && a.Reason == b.Reason && a.Tools == b.Tools
		})
		model.mcpStatus = statuses
		for _, status := range statuses {
			if status.State == "connecting" || status.State == "unverified" {
				// Wait for background discovery before starting the visibility countdown.
				model.mcpStatusUntil = time.Time{}
				return
			}
		}
		if changed || model.mcpStatusUntil.IsZero() {
			model.mcpStatusUntil = now.Add(10 * time.Second)
		}
		if !now.Before(model.mcpStatusUntil) {
			model.mcpStatusHidden = true
			model.mcpStatus = nil
		}
	}
}

func (model Model) mcpStatusLabel() string {
	if model.mcpStatusHidden || (!model.mcpStatusUntil.IsZero() && !time.Now().Before(model.mcpStatusUntil)) {
		return ""
	}
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
