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

func TestToolDrawerToggleRestoresSingleRowAndSingleHistory(t *testing.T) {
	model := toolDrawerModel(t, 80)
	collapsed := model.View().Content
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
		if i%2 == 0 && (rows != expandedRows || rows != strings.Count(collapsed, "\n")+1 || !strings.Contains(content, "file-001.go")) {
			t.Fatalf("expanded drawer changed frame height or hid tool rows: %d", rows)
		}
		if i%2 == 1 && content != collapsed {
			t.Fatalf("collapse did not restore compact frame: %q", content)
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
	// The summary remains clickable after the frame shrinks.
	lines := strings.Split(model.View().Content, "\n")
	header := 0
	for !strings.Contains(lines[header], "工具活动") {
		header++
	}
	updated, _ := model.Update(tea.MouseClickMsg{X: 1, Y: model.height - len(lines) + header, Button: tea.MouseLeft})
	model = updated.(Model)
	if !model.tools.expanded {
		t.Fatal("collapsed summary was not clickable")
	}
	model.commitToolGroup()
	model.commitToolGroup()
	history := strings.Join(model.printQueue, "\n")
	if strings.Count(history, "工具活动") != 1 || strings.Count(history, "file-001.go") != 1 || strings.Count(history, "file-080.go") != 1 {
		t.Fatal("history lost or duplicated tool details")
	}
}

func TestToolDrawerCollapseRestoresCompactLayout(t *testing.T) {
	for _, method := range []string{"keyboard", "escape", "mouse"} {
		for _, stream := range []string{"", "正在生成回复\n下一行"} {
			t.Run(method+"/"+stream, func(t *testing.T) {
				model := toolDrawerModel(t, 80)
				model.stream = stream
				before := model.View()
				if got, want := len(model.liveLines()), 1+len(model.streamLines(model.contentWidth()))+len(model.footerLines()); got != want {
					t.Fatalf("collapsed tool area must occupy one row: got %d rows, want %d", got, want)
				}
				updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
				model = updated.(Model)
				var collapse tea.Msg = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
				switch method {
				case "escape":
					collapse = tea.KeyPressMsg{Code: tea.KeyEscape}
				case "mouse":
					collapse = tea.MouseClickMsg{X: 1, Y: model.height - len(model.liveLines()), Button: tea.MouseLeft}
				}
				updated, _ = model.Update(collapse)
				model = updated.(Model)
				after := model.View()
				if model.tools.expanded || after.Content != before.Content {
					t.Fatalf("collapse left extra rows or lost content: %q", after.Content)
				}
				if before.Cursor == nil || after.Cursor == nil || after.Cursor.Position != before.Cursor.Position {
					t.Fatalf("collapse did not restore input cursor: before=%+v, after=%+v", before.Cursor, after.Cursor)
				}
				assertLiveFrameFits(t, model)
			})
		}
	}
}

func TestToolDrawerAutoCollapseRestoresSingleRow(t *testing.T) {
	model := toolDrawerModel(t, toolCollapseThreshold-1)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, events: make(chan turnEvent), cancel: func() {}}
	for i := toolCollapseThreshold; i <= toolCollapseThreshold+1; i++ {
		updated, _ := model.Update(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{Kind: "tool_start", ID: fmt.Sprint(i), Name: "read_file"}})
		model = updated.(Model)
	}
	lines := model.liveLines()
	if len(lines) != 1+len(model.footerLines()) || !strings.Contains(lines[0], "工具活动") {
		t.Fatalf("automatic collapse left blank rows: %q", model.View().Content)
	}
	assertLiveFrameFits(t, model)
}

func TestToolDrawerWithoutPagingResizeAndLongStreamStayWithinTerminal(t *testing.T) {
	model := toolDrawerModel(t, 60)
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	model = updated.(Model)
	before := model.View().Content
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}} {
		updated, _ = model.Update(key)
		model = updated.(Model)
		if model.View().Content != before {
			t.Fatal("page keys must not change tool details")
		}
		assertLiveFrameFits(t, model)
	}
	if strings.Contains(before+model.helpText(), "翻页") || strings.Contains(before+model.helpText(), "PgUp") || strings.Contains(before, "详情 1") {
		t.Fatal("tool drawer still advertises paging")
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
