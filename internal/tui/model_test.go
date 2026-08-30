package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type fakeBackend struct {
	history  History
	commands []Command
}

func (backend *fakeBackend) RunTurn(context.Context, string, string, TurnEvents) (TurnResult, error) {
	return TurnResult{}, nil
}
func (backend *fakeBackend) RuntimeInfo(string) RuntimeInfo {
	return RuntimeInfo{Provider: "test", Model: "model"}
}
func (backend *fakeBackend) History(string, int) (History, error) { return backend.history, nil }
func (backend *fakeBackend) Commands() []Command                  { return backend.commands }

func TestModelStreamingToolQueueAndLateEvents(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir()})
	model.width = 60
	_ = model.submit("第一轮")
	events := make(chan turnEvent)
	model.active = &activeTurn{sequence: model.sequence, events: events, cancel: func() {}}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "token", text: "**你好**"})
	if model.phase != phaseStreaming || !strings.Contains(model.stream, "你好") {
		t.Fatalf("stream state = %v, %q", model.phase, model.stream)
	}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "tool", tool: ToolEvent{Kind: "tool_start", Seq: 1, Name: "read_file"}})
	if model.phase != phaseTool || model.stream != "" {
		t.Fatalf("tool state = %v, stream=%q", model.phase, model.stream)
	}
	model.queue("下一条")
	commands := model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", result: TurnResult{SessionID: "session-1", Streamed: true}})
	if len(commands) == 0 || model.phase != phaseThinking || model.queued != nil {
		t.Fatalf("queued turn was not promoted: commands=%d phase=%v queued=%#v", len(commands), model.phase, model.queued)
	}
	before := model.stream
	model.handleTurnEvent(turnEvent{sequence: model.sequence - 1, kind: "token", text: "late"})
	if model.stream != before {
		t.Fatal("late event changed stream")
	}
}

func TestModelContinuousTokenUpdatesDoNotCopyBuilder(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir()})
	model.sequence = 1
	events := make(chan turnEvent)
	model.active = &activeTurn{sequence: 1, events: events, cancel: func() {}}
	updated, _ := model.Update(turnEvent{sequence: 1, kind: "token", text: "第一段"})
	model = updated.(Model)
	updated, _ = model.Update(turnEvent{sequence: 1, kind: "token", text: "第二段"})
	model = updated.(Model)
	if model.stream != "第一段第二段" {
		t.Fatalf("stream = %q", model.stream)
	}
}

func TestToolGroupCollapsesAfterFiveAndMouseExpands(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width = 70
	model.height = 24
	model.sequence = 1
	events := make(chan turnEvent)
	model.active = &activeTurn{sequence: 1, events: events, cancel: func() {}}
	for index := 1; index <= 6; index++ {
		id := fmt.Sprintf("tool-%d", index)
		model.handleTurnEvent(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{Kind: "tool_start", Seq: uint64(index*2 - 1), ID: id, Name: "read_file", Arguments: map[string]any{"path": fmt.Sprintf("file-%d.go", index)}}})
		if index < 6 {
			model.handleTurnEvent(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{Kind: "tool_result", Seq: uint64(index * 2), ID: id, Name: "read_file", Status: "succeeded", DurationMS: 2}})
		}
	}
	collapsed := model.View().Content
	if !strings.Contains(collapsed, "工具活动 · 6 次") || !strings.Contains(collapsed, "点击或 Ctrl+T 展开") || strings.Contains(collapsed, "file-1.go") {
		t.Fatalf("collapsed tool group = %q", collapsed)
	}
	viewLines := strings.Count(collapsed, "\n") + 1
	headerY := max(0, model.height-viewLines)
	updated, _ := model.Update(tea.MouseClickMsg{X: 1, Y: headerY, Button: tea.MouseLeft})
	model = updated.(Model)
	expanded := model.View().Content
	if !model.tools.expanded || !strings.Contains(expanded, "file-1.go") || !strings.Contains(expanded, "收起详情") {
		t.Fatalf("expanded tool group = %q", expanded)
	}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Streamed: true}})
	if len(model.tools.items) != 6 {
		t.Fatal("completed turn should keep the tool drawer available")
	}
	_ = model.submit("下一轮")
	if len(model.tools.items) != 0 {
		t.Fatal("next turn should commit and clear the previous tool drawer")
	}
}

func TestModelCancelRestoresQueueAndCommands(t *testing.T) {
	backend := &fakeBackend{commands: []Command{{Name: "/model", Arguments: []CommandArgument{{Value: "fast", Description: "fast model"}}}}}
	model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir()})
	model.width = 60
	_ = model.submit("第一轮")
	events := make(chan turnEvent)
	model.active = &activeTurn{sequence: model.sequence, events: events, cancel: func() {}, cancelled: true}
	model.queued = &queuedPrompt{value: "恢复我"}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", err: context.Canceled})
	if model.input.value() != "恢复我" || model.queued != nil {
		t.Fatalf("cancel restore = %q, queued=%#v", model.input.value(), model.queued)
	}
	model.input.setValue("/model ")
	model.refreshSuggestions()
	if !model.panelOpen || len(model.suggestions) != 1 || model.suggestions[0].value != "/model fast" {
		t.Fatalf("suggestions = %#v", model.suggestions)
	}
	model.input.setValue("普通")
	model.active = &activeTurn{cancel: func() {}}
	model.queued = &queuedPrompt{value: "/model fast"}
	model.queue("普通消息")
	if model.input.value() != "普通消息" || model.queued.value != "/model fast" {
		t.Fatal("slash boundary should preserve queued command and restore new draft")
	}
}

func TestEscapeCancelsActiveTurn(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir()})
	cancelled := false
	model.active = &activeTurn{cancel: func() { cancelled = true }}
	model.phase = phaseStreaming

	commands := model.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if len(commands) != 0 || !cancelled || !model.active.cancelled {
		t.Fatalf("escape cancellation: commands=%d cancelCalled=%v active=%#v", len(commands), cancelled, model.active)
	}
	if model.phase != phaseCancelling || model.progress != "正在取消" {
		t.Fatalf("escape phase=%v progress=%q", model.phase, model.progress)
	}
}

func TestClipboardShortcutsAndCtrlQExit(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.input.setValue("copy me")
	model.input.area.SelectAll()
	cancelled := false
	model.active = &activeTurn{cancel: func() { cancelled = true }}

	copyCommands := model.handleKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if len(copyCommands) != 1 || copyCommands[0] == nil {
		t.Fatalf("Ctrl+C commands = %#v, want clipboard copy command", copyCommands)
	}
	if cancelled || model.active.cancelled {
		t.Fatal("Ctrl+C must copy instead of cancelling the active turn")
	}

	pasteCommands := model.handleKey(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if len(pasteCommands) != 1 || pasteCommands[0] == nil {
		t.Fatalf("Ctrl+V commands = %#v, want clipboard read command", pasteCommands)
	}

	quitCommands := model.handleKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if len(quitCommands) != 0 || !cancelled || !model.active.cancelled || !model.pendingQuit {
		t.Fatalf("Ctrl+Q active exit: commands=%d cancelled=%v active=%#v pendingQuit=%v", len(quitCommands), cancelled, model.active, model.pendingQuit)
	}
	if model.progress != "正在安全退出" {
		t.Fatalf("Ctrl+Q progress = %q", model.progress)
	}
}

func TestClipboardPasteMessageReachesPromptInput(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	updated, _ := model.Update(tea.PasteMsg{Content: "来自剪贴板"})
	model = updated.(Model)
	if model.input.value() != "来自剪贴板" {
		t.Fatalf("input after paste = %q", model.input.value())
	}
}

func TestCtrlQExitsImmediatelyWhenIdle(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	commands := model.handleKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	foundQuit := false
	for _, command := range commands {
		if command == nil {
			continue
		}
		if _, ok := command().(tea.QuitMsg); ok {
			foundQuit = true
		}
	}
	if !foundQuit {
		t.Fatalf("Ctrl+Q commands = %#v, want tea.Quit", commands)
	}
}

func TestPromptChipHistoryMarkdownAndNarrowResize(t *testing.T) {
	input := newPromptInput()
	input.update(tea.KeyPressMsg{Code: tea.KeyExtended, Text: "输入"})
	if input.value() != "输入" || !strings.Contains(input.area.View(), "输入") {
		t.Fatalf("Chinese input = value %q view %q", input.value(), input.area.View())
	}
	input.reset()
	paste := strings.Repeat("长文本", 80)
	input.update(tea.PasteMsg{Content: paste})
	if len(input.chips) != 1 || input.value() != paste || strings.Contains(input.displayValue(), paste) {
		t.Fatalf("chip = %#v display=%q", input.chips, input.displayValue())
	}
	input.update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if input.displayValue() != "" || len(input.chips) != 0 {
		t.Fatalf("chip backspace was not atomic: display=%q chips=%#v", input.displayValue(), input.chips)
	}
	store := newHistoryStore(t.TempDir(), "D:/中文/工作区")
	entries, err := store.append(nil, paste)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.load()
	if err != nil || len(entries) != 1 || len(loaded) != 1 || loaded[0] != paste {
		t.Fatalf("history entries=%d loaded=%d err=%v", len(entries), len(loaded), err)
	}
	for index := 0; index < historyMaxEntries+5; index++ {
		entries, err = store.append(entries, fmt.Sprintf("entry-%03d", index))
		if err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(store.path)
	if err != nil || len(entries) != historyMaxEntries || info.Size() > historyMaxBytes {
		t.Fatalf("history retention entries=%d size=%d err=%v", len(entries), info.Size(), err)
	}
	rendered := renderMarkdown("| 列 | 值 |\n|---|---|\n| 中文 | **好** |", 24, true)
	if !strings.Contains(rendered, "中文") || strings.Contains(rendered, "\x1b[") {
		t.Fatalf("markdown = %q", rendered)
	}
	todo := toolSummary(ToolEvent{Kind: "tool_start", Seq: 2, Name: "todo_write", Arguments: map[string]any{"todos": `[{"content":"完成 TUI","status":"in_progress"}]`}}, newTheme(true))
	if !strings.Contains(todo, "任务进度 0/1") || !strings.Contains(todo, "完成 TUI") {
		t.Fatalf("todo summary = %q", todo)
	}
	failure := toolSummary(ToolEvent{Kind: "tool_result", Seq: 3, Name: "bash", Status: "failed", Preview: strings.Repeat("错误\n", 20)}, newTheme(true))
	if strings.Count(failure, "\n") > 9 || len(failure) > 2200 {
		t.Fatalf("failure preview was not bounded: lines=%d bytes=%d", strings.Count(failure, "\n")+1, len(failure))
	}
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 12, Height: 8})
	model = updated.(Model)
	if model.contentWidth() != 10 {
		t.Fatalf("narrow content width = %d", model.contentWidth())
	}
	if strings.Contains(model.View().Content, "\x1b[") {
		t.Fatalf("NO_COLOR view contains ANSI: %q", model.View().Content)
	}
}

func TestInitialTaskAutoSubmits(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), InitialTask: "自动任务"})
	updated, command := model.Update(startupMsg{})
	got := updated.(Model)
	if command == nil || got.phase != phaseThinking || got.active == nil || !got.initialSent {
		t.Fatalf("initial task state: command=%v phase=%v active=%#v sent=%v", command != nil, got.phase, got.active, got.initialSent)
	}
}

func TestTurnPresentationUsesSeparatorsWithoutRepeatedBrand(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width = 30
	startup := strings.Join(model.startup, "\n")
	if !strings.Contains(startup, "bqAgent  Harness") || strings.Contains(startup, "内联终端助手") {
		t.Fatalf("startup = %q", startup)
	}

	_ = model.submit("第一轮")
	model.stream = "回答内容"
	if view := model.View().Content; strings.Contains(view, "bqAgent") || !strings.Contains(view, "回答内容") {
		t.Fatalf("streaming view = %q", view)
	}
	model.flushStream()
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", result: TurnResult{Reply: "非流式回答"}})
	printed := strings.Join(model.printQueue, "\n")
	separator := strings.Repeat("─", model.contentWidth())
	if !strings.Contains(printed, separator+"\n▸ 第一轮") || !strings.Contains(printed, "回答内容") || !strings.Contains(printed, "非流式回答") {
		t.Fatalf("turn output = %q", printed)
	}
	if strings.Contains(printed, "bqAgent") {
		t.Fatalf("turn output repeats brand = %q", printed)
	}

	history := model.renderHistory(History{ID: "session-1", Messages: []HistoryMessage{
		{Role: "user", Content: "历史问题"},
		{Role: "assistant", Content: "历史回答"},
	}})
	restored := strings.Join(history, "\n")
	if !strings.Contains(restored, separator+"\n▸ 历史问题") || strings.Contains(restored, "bqAgent") {
		t.Fatalf("restored history = %q", restored)
	}

	tools := model.renderHistory(History{ID: "session-2", Messages: []HistoryMessage{
		{Role: "user", Content: "写入全局记忆"},
		{Role: "assistant", Tools: []ToolEvent{{ID: "call-1", Name: "memory", Status: "succeeded", Preview: `{"target":"global"}`}}},
	}})
	restoredTools := strings.Join(tools, "\n")
	if strings.Contains(restoredTools, "Completed tool activity") || !strings.Contains(restoredTools, "工具活动") || !strings.Contains(restoredTools, "memory") {
		t.Fatalf("restored tool history = %q", restoredTools)
	}
}

func TestUserMessageTriangleStyle(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir()})
	rendered := model.renderUserMessage("第一行\n第二行")
	if !strings.Contains(rendered, "\x1b[") || !strings.Contains(rendered, "▸") {
		t.Fatalf("triangle should be colored: %q", rendered)
	}
	if !strings.Contains(rendered, "\n  第二行") {
		t.Fatalf("continuation should align with the question: %q", rendered)
	}
}

func TestCompletedStatusShowsCacheHitRate(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width = 100
	model.metrics = &GenerationMetrics{
		FirstTokenLatencyMS: 250,
		PromptTokens:        100,
		CachedPromptTokens:  75,
		CacheUsageAvailable: true,
		CompletionTokens:    20,
		TokensPerSecond:     10,
		CacheMetrics: &CacheMetrics{
			Available: true, Calls: 2, InputTokens: 200,
			CacheReadTokens: 50, CacheWriteTokens: 25, UncachedInputTokens: 125, HitRate: .25,
		},
	}
	status := model.renderStatus(model.contentWidth())
	if !strings.Contains(status, "缓存命中 25%") || strings.Contains(status, "缓存命中 75%") {
		t.Fatalf("status = %q", status)
	}
}

func TestMergeCommandsIncludesRunAndAskModes(t *testing.T) {
	commands := mergeCommands(nil)
	found := map[string]bool{}
	for _, command := range commands {
		if command.Name == "/ask" && strings.Contains(command.Description, "只读问答") {
			found["ask"] = true
		}
		if command.Name == "/run" && strings.Contains(command.Description, "完整工具") {
			found["run"] = true
		}
	}
	if !found["ask"] || !found["run"] {
		t.Fatalf("mode commands = %#v, want /run and /ask", found)
	}
}

func TestRenderStatusShowsCurrentMode(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width = 100
	model.runtime.Mode = "ask"
	if status := model.renderStatus(model.contentWidth()); !strings.Contains(status, "Ask") {
		t.Fatalf("ask status = %q", status)
	}
	model.runtime.Mode = "run"
	if status := model.renderStatus(model.contentWidth()); !strings.Contains(status, "Run") {
		t.Fatalf("run status = %q", status)
	}
}

func TestCompletedTurnUpdatesDisplayedMode(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, cancel: func() {}}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Mode: "ask"}})
	if model.runtime.Mode != "ask" {
		t.Fatalf("runtime mode = %q, want ask", model.runtime.Mode)
	}
}

func TestStartupOmitsDynamicModelAndModeLines(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.runtime.Provider = "provider"
	model.runtime.Model = "dynamic-model"
	model.runtime.Mode = "ask"
	startup := strings.Join(model.startupLines(), "\n")
	if strings.Contains(startup, "模型    ") || strings.Contains(startup, "模式    ") {
		t.Fatalf("startup contains stale dynamic lines: %q", startup)
	}
	if !strings.Contains(startup, "工作区  "+model.config.Workspace) {
		t.Fatalf("startup omits workspace: %q", startup)
	}
}

func TestMergeCommandsRefreshReplacesExistingCommand(t *testing.T) {
	commands := mergeCommands([]Command{
		{Name: " /ASK ", Description: "第一次刷新", Arguments: []CommandArgument{{Value: "old"}}},
		{Name: "/ask", Description: "最新刷新"},
	})
	count := 0
	for _, command := range commands {
		if normalizedCommandName(command.Name) != "/ask" {
			continue
		}
		count++
		if command.Description != "最新刷新" || len(command.Arguments) != 0 {
			t.Fatalf("ask command = %#v, want latest complete replacement", command)
		}
	}
	if count != 1 {
		t.Fatalf("ask command count = %d, want 1", count)
	}
}

func TestRefreshSuggestionsDeduplicatesEquivalentCommands(t *testing.T) {
	backend := &fakeBackend{commands: []Command{
		{Name: "/ASK", Description: "第一次刷新"},
		{Name: " /ask ", Description: "最新刷新"},
	}}
	model := NewModel(backend, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir()})
	model.input.setValue("/ask")
	model.refreshSuggestions()

	if len(model.suggestions) != 1 || model.suggestions[0].value != "/ask" || model.suggestions[0].description != "最新刷新" {
		t.Fatalf("suggestions = %#v, want one latest /ask entry", model.suggestions)
	}
}

func TestSuggestionFrameHeightStaysStableWhileTypingCommand(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width = 100
	wantLines := 0
	for _, input := range []string{"/", "/a", "/as", "/ask"} {
		model.input.setValue(input)
		model.refreshSuggestions()
		if !model.panelOpen {
			t.Fatalf("panel is closed for %q", input)
		}
		content := model.View().Content
		if count := strings.Count(content, "命令与参数"); count != 1 {
			t.Fatalf("heading count for %q = %d, want 1", input, count)
		}
		lines := strings.Count(content, "\n") + 1
		if wantLines == 0 {
			wantLines = lines
		} else if lines != wantLines {
			t.Fatalf("view line count for %q = %d, want stable %d", input, lines, wantLines)
		}
	}
}
