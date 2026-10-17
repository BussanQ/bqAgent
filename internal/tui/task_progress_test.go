package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bqagent/internal/taskstate"
	"bqagent/internal/turn"
)

func tuiTaskFixture() *taskstate.State {
	return &taskstate.State{
		Goal: "修复运行中排队", Status: "active",
		Criteria:  []taskstate.Criterion{{Description: "输入不丢失", Done: true, Evidence: "定向测试通过"}},
		Remaining: []string{"修改后重新验证"},
	}
}

func taskSnapshot(t *testing.T, state *taskstate.State, seq uint64) turn.Event {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return turn.Event{TurnID: "turn", ItemID: "turn/task", Kind: "task.updated", Seq: seq, Text: string(data)}
}

func TestTaskProgressSnapshotUpdatesUIWithoutAssistantContent(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	model.height = 24
	model.stream = "正在实现"
	event := taskSnapshot(t, tuiTaskFixture(), 2)
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: event})
	view := strings.Join(model.auxiliaryLines(), "\n")
	for _, want := range []string{"任务 · 进行中", "验收 1/1", "修复运行中排队", "[✓] 输入不丢失", "修改后重新验证"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
	}
	if strings.Contains(view, "已完成") || model.stream != "正在实现" || model.liveTokens != 0 || model.messages.Latest.Text != "" {
		t.Fatalf("task state leaked into assistant or inferred completion: view=%s model=%+v", view, model.messages)
	}
	before := view
	for _, invalid := range []turn.Event{
		taskSnapshot(t, nil, 1), taskSnapshot(t, nil, 2),
		{Kind: "task.updated", TurnID: "other", ItemID: "other/task", Seq: 3, Text: "null"},
		{Kind: "task.updated", TurnID: "turn", ItemID: "turn/task", Seq: 3, Text: "broken"},
	} {
		model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: invalid})
		if got := strings.Join(model.auxiliaryLines(), "\n"); got != before {
			t.Fatalf("invalid or stale event changed task: %+v\n%s", invalid, got)
		}
	}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: taskSnapshot(t, nil, 3)})
	if model.task.state != nil || len(model.auxiliaryLines()) != 0 {
		t.Fatal("null snapshot did not clear task")
	}
}

func TestTaskProgressRestoresHistoryAndClearsNewSession(t *testing.T) {
	backend := &fakeBackend{history: History{Task: tuiTaskFixture()}}
	model := NewModel(backend, Config{Context: context.Background(), SessionID: "session", Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	if model.task.state == nil || !strings.Contains(strings.Join(model.auxiliaryLines(), "\n"), "修复运行中排队") {
		t.Fatal("resumed history did not restore task")
	}
	model.clearSession()
	if model.task.state != nil || len(model.auxiliaryLines()) != 0 {
		t.Fatal("new session retained previous task")
	}
	model = NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	if len(model.auxiliaryLines()) != 0 {
		t.Fatal("ordinary conversation should not create a task panel")
	}
}

func TestTaskProgressStatusAndLayoutRemainAuthoritative(t *testing.T) {
	for _, tc := range []struct{ status, label string }{
		{"active", "进行中"}, {"completed", "已完成"}, {"blocked", "受阻"}, {"awaiting_input", "等待输入"}, {"cancelled", "已取消"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			model := newInputModel(t, &inputFakeBackend{})
			state := tuiTaskFixture()
			state.Status, state.Reason = tc.status, "阶段变化"
			if tc.status == "completed" {
				state.Remaining = nil
				state.Verification = []taskstate.Verification{{Command: "go test -run TestQueue", Status: "passed", Details: "ok"}}
			}
			model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: taskSnapshot(t, state, 1)})
			if !strings.Contains(strings.Join(model.auxiliaryLines(), "\n"), "任务 · "+tc.label) {
				t.Fatal("status not projected")
			}
			for _, height := range []int{8, 24} {
				model.height = height
				model.stream = "回答正文"
				model.input.setValue("后续草稿")
				lines, _ := model.liveLayout()
				if len(lines) != model.frameLimit() || !strings.Contains(strings.Join(lines, "\n"), "后续草稿") {
					t.Fatalf("task panel broke frame at height %d: %v", height, lines)
				}
			}
		})
	}
}

func TestTaskProgressMarksStaleSnapshotUntilNextRecord(t *testing.T) {
	model := newInputModel(t, &inputFakeBackend{})
	stale := taskSnapshot(t, tuiTaskFixture(), 1)
	stale.Phase = "stale"
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: stale})
	if view := strings.Join(model.auxiliaryLines(), "\n"); !strings.Contains(view, "任务 · 进行中（记录未更新） · 验收 1/1") {
		t.Fatalf("stale snapshot not marked: %s", view)
	}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "message", message: taskSnapshot(t, tuiTaskFixture(), 2)})
	if view := strings.Join(model.auxiliaryLines(), "\n"); strings.Contains(view, "记录未更新") || !strings.Contains(view, "任务 · 进行中") {
		t.Fatalf("fresh snapshot kept stale marker: %s", view)
	}
}
