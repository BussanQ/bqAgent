package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	collapsed := model.View()
	if !strings.Contains(collapsed, "工具活动 · 6 次") || !strings.Contains(collapsed, "点击或 Ctrl+T 展开") || strings.Contains(collapsed, "file-1.go") {
		t.Fatalf("collapsed tool group = %q", collapsed)
	}
	viewLines := strings.Count(collapsed, "\n") + 1
	headerY := max(0, model.height-viewLines)
	updated, _ := model.Update(tea.MouseMsg{X: 1, Y: headerY, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	model = updated.(Model)
	expanded := model.View()
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

func TestPromptChipHistoryMarkdownAndNarrowResize(t *testing.T) {
	input := newPromptInput()
	paste := strings.Repeat("长文本", 80)
	input.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	if len(input.chips) != 1 || input.value() != paste || strings.Contains(input.displayValue(), paste) {
		t.Fatalf("chip = %#v display=%q", input.chips, input.displayValue())
	}
	input.update(tea.KeyMsg{Type: tea.KeyBackspace})
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
	if strings.Contains(model.View(), "\x1b[") {
		t.Fatalf("NO_COLOR view contains ANSI: %q", model.View())
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
