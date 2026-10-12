package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func toolDrawerModel(t *testing.T, count int) Model {
	t.Helper()
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.width, model.height = 72, 24
	model.resizeInput()
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("call-%d", i)
		model.tools.add(ToolEvent{Kind: "tool_start", ID: id, Seq: uint64(i), Name: "read_file", Arguments: map[string]any{"path": fmt.Sprintf("file-%03d.go", i)}})
		model.tools.add(ToolEvent{Kind: "tool_result", ID: id, Name: "read_file", Status: "succeeded"})
	}
	model.toolMouse = count > toolCollapseThreshold
	return model
}

func assertLiveFrameFits(t *testing.T, model Model) {
	t.Helper()
	view := model.View()
	lines := strings.Split(view.Content, "\n")
	if len(lines) > max(1, model.height-1) {
		t.Fatalf("live frame escaped terminal: %d rows, terminal height %d", len(lines), model.height)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > model.width {
			t.Fatalf("implicit line wrap: width=%d, line=%q", model.width, line)
		}
	}
	if cursor := view.Cursor; cursor != nil && (cursor.Position.Y < 0 || cursor.Position.Y >= len(lines) || cursor.Position.X >= model.width) {
		t.Fatalf("cursor outside live frame: %+v", cursor.Position)
	}
}

func TestToolDrawerToggleKeepsBoundedStableFrameAndSingleHistory(t *testing.T) {
	model := toolDrawerModel(t, 80)
	var expandedRows int
	for i := 0; i < 6; i++ {
		updated, cmd := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		model = updated.(Model)
		if cmd != nil {
			t.Fatal("toggle must not emit history or terminal commands")
		}
		assertLiveFrameFits(t, model)
		content := model.View().Content
		rows := strings.Count(content, "\n") + 1
		if i == 0 {
			expandedRows = rows
		}
		if rows != expandedRows {
			t.Fatalf("toggle resized the inline frame: %d != %d", rows, expandedRows)
		}
		if strings.Count(content, "工具活动") != 1 {
			t.Fatalf("duplicated drawer: %q", content)
		}
		if i%2 == 1 && strings.Contains(content, "file-001.go") {
			t.Fatal("collapsed details remained visible")
		}
		if len(model.tools.items) != 80 {
			t.Fatal("toggle mutated tool records")
		}
	}
	// Click hit testing includes the blank rows retained after collapse.
	lines := strings.Split(model.View().Content, "\n")
	header := 0
	for !strings.Contains(lines[header], "工具活动") {
		header++
	}
	updated, _ := model.Update(tea.MouseClickMsg{X: 1, Y: model.height - len(lines) + header, Button: tea.MouseLeft})
	model = updated.(Model)
	if !model.tools.expanded {
		t.Fatal("padded summary was not clickable")
	}
	model.commitToolGroup()
	model.commitToolGroup()
	history := strings.Join(model.printQueue, "\n")
	if strings.Count(history, "工具活动") != 1 || strings.Count(history, "file-001.go") != 1 || strings.Count(history, "file-080.go") != 1 {
		t.Fatal("history lost or duplicated tool details")
	}
}

func TestToolDrawerPagingResizeAndLongStreamStayWithinTerminal(t *testing.T) {
	model := toolDrawerModel(t, 60)
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	model = updated.(Model)
	for i := 0; i < 100 && !strings.Contains(model.View().Content, "file-060.go"); i++ {
		updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
		model = updated.(Model)
		assertLiveFrameFits(t, model)
	}
	if !strings.Contains(model.View().Content, "file-060.go") {
		t.Fatal("last tool is unreachable")
	}
	for i := 0; i < 100 && !strings.Contains(model.View().Content, "file-001.go"); i++ {
		updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
		model = updated.(Model)
	}
	if !strings.Contains(model.View().Content, "file-001.go") {
		t.Fatal("first tool is unreachable")
	}
	text := strings.Repeat("长回复🙂带有很长的行", 200) + "\n最后一行"
	model.stream = text
	model.input.setValue("/model")
	model.refreshSuggestions()
	model.queued = &queuedPrompt{value: "下一轮任务"}
	for _, size := range [][2]int{{42, 14}, {18, 9}, {8, 5}, {72, 24}} {
		updated, _ = model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		model = updated.(Model)
		assertLiveFrameFits(t, model)
		if model.stream != text {
			t.Fatal("viewport clipping lost response text")
		}
	}
	model.flushStream()
	model.flushStream()
	if len(model.printQueue) != 1 || !strings.Contains(model.printQueue[0], "最后一行") {
		t.Fatal("stream must commit exactly once")
	}
}

func TestToolEventsMergeReplayedAndOutOfOrderCalls(t *testing.T) {
	model := toolDrawerModel(t, 0)
	result := ToolEvent{Kind: "tool_result", ID: "same", Name: "read_file", Status: "failed", Preview: "first failure"}
	model.tools.add(result)
	_ = model.View() // Populate the render cache before the correction.
	start := ToolEvent{Kind: "tool_start", ID: "same", Name: "read_file", Seq: 7, Arguments: map[string]any{"path": "original.go"}}
	model.tools.add(start)
	model.tools.add(start)
	result.Status, result.Preview = "succeeded", ""
	model.tools.add(result)
	model.tools.add(result)
	view := model.View().Content
	if len(model.tools.items) != 1 || strings.Count(view, "original.go") != 1 || strings.Contains(view, "first failure") || !strings.Contains(view, "✓ #7") {
		t.Fatalf("replayed call wasn't updated in place: %q", view)
	}
	start.ID = "different"
	model.tools.add(start)
	if len(model.tools.items) != 2 || !strings.Contains(model.View().Content, "运行中") {
		t.Fatal("distinct call was merged or cache was stale")
	}
}

func TestIdleSpinnerStopsAndActiveSpinnerContinues(t *testing.T) {
	model := toolDrawerModel(t, 6)
	_, command := model.Update(model.spinner.Tick())
	if command != nil {
		t.Fatal("idle spinner scheduled another animation frame")
	}
	model.active = &activeTurn{cancel: func() {}}
	updated, command := model.Update(model.spinner.Tick())
	if command == nil {
		t.Fatal("active spinner did not schedule its next frame")
	}
	model = updated.(Model)
	model.active = nil
	_, command = model.Update(model.spinner.Tick())
	if command != nil {
		t.Fatal("completed turn kept the spinner running")
	}
}

func BenchmarkToolDrawerUnchangedFrame(b *testing.B) {
	model := NewModel(&fakeBackend{}, Config{Workspace: b.TempDir(), AgentDir: b.TempDir(), NoColor: true})
	model.width, model.height = 80, 24
	model.resizeInput()
	for i := 0; i < 1000; i++ {
		model.tools.add(ToolEvent{Kind: "tool_start", ID: fmt.Sprint(i), Name: "read_file", Arguments: map[string]any{"path": "a-long-file-name.go"}})
	}
	model.tools.expanded = true
	_ = model.View()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = model.View()
	}
}
