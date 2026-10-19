package tui

import (
	"strings"
	"testing"

	"bqagent/internal/taskstate"
	"bqagent/internal/turn"
)

func TestTaskProgressAcceptsCompletedAnalysisWithoutTests(t *testing.T) {
	var progress taskProgress
	if !progress.apply(turn.Event{
		TurnID: "analysis", ItemID: "analysis/task", Seq: 1, Kind: "task.updated", Phase: "completed",
		Text: `{"kind":"analysis","goal":"分析项目","status":"completed","criteria":[{"description":"梳理入口并给出报告","done":true,"evidence":"阅读 cmd/agent/main.go，报告说明入口与模块关系"}],"remaining":[]}`,
	}) {
		t.Fatal("completed analysis snapshot rejected without test verification")
	}
	lines := progress.lines()
	if len(lines) < 2 || !strings.Contains(lines[0], "已完成") || !strings.Contains(lines[0], "验收 1/1") {
		t.Fatalf("analysis completion not reflected in task panel: %v", lines)
	}
}

func TestTaskProgressClearsPreviousTaskOnSubmit(t *testing.T) {
	// Any status: a task left over from the previous turn, finished or not,
	// must not be shown beside the new turn.
	for _, status := range []string{"completed", "cancelled", "active", "blocked", "awaiting_input"} {
		t.Run(status, func(t *testing.T) {
			model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.task = taskProgress{state: &taskstate.State{Goal: "上一轮任务", Status: status, Criteria: []taskstate.Criterion{{Description: "验收"}}}, turnID: "previous", seq: 8}
			_ = model.submit("下一轮")
			defer model.active.cancel()
			if model.task.state != nil {
				t.Fatalf("status %s: leftover task = %+v", status, model.task.state)
			}
			if len(model.auxiliaryLines()) != 0 {
				t.Fatalf("status %s: leftover task still visible: %v", status, model.auxiliaryLines())
			}
			if model.task.turnID != "" || model.task.seq != 0 {
				t.Fatal("previous turn event identity retained")
			}
		})
	}
}

func TestResumeSkipsFinishedTaskInLivePanel(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "active", "waiting"} {
		t.Run(status, func(t *testing.T) {
			live := status
			if live == "waiting" {
				live = "awaiting_input"
			}
			backend := &fakeBackend{history: History{ID: "saved", Task: &taskstate.State{Goal: "旧任务", Status: live, Criteria: []taskstate.Criterion{{Description: "验收"}}}}}
			model := NewModel(backend, Config{SessionID: "saved", Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			finished := live == "completed" || live == "cancelled"
			if (model.task.state != nil) == finished {
				t.Fatalf("status %s: live task = %+v", live, model.task.state)
			}
			if finished && len(model.auxiliaryLines()) != 0 {
				t.Fatalf("status %s: resumed finished task visible: %v", live, model.auxiliaryLines())
			}
		})
	}
}

func TestTaskProgressLinesShowsAllCriteria(t *testing.T) {
	progress := taskProgress{state: &taskstate.State{
		Goal: "显示完整任务列表", Status: "active",
		Criteria: []taskstate.Criterion{
			{Description: "第一项", Done: true},
			{Description: "第二项"},
			{Description: "第三项", Done: true},
			{Description: "第四项"},
		},
	}}
	lines := progress.lines()
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want header and all four criteria: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "验收 2/4") {
		t.Fatalf("incorrect summary: %s", lines[0])
	}
	for i, want := range []string{"  [✓] 第一项", "  [ ] 第二项", "  [✓] 第三项", "  [ ] 第四项"} {
		if lines[i+1] != want {
			t.Errorf("line %d = %q, want %q", i+1, lines[i+1], want)
		}
	}
}
