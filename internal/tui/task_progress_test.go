package tui

import (
	"strings"
	"testing"

	"bqagent/internal/taskstate"
)

func TestTaskProgressClearsFinishedTaskOnSubmit(t *testing.T) {
	for _, status := range []string{"completed", "cancelled", "active", "blocked", "awaiting_input"} {
		t.Run(status, func(t *testing.T) {
			model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
			model.task = taskProgress{state: &taskstate.State{Goal: "上一轮任务", Status: status, Criteria: []taskstate.Criterion{{Description: "验收"}}}, turnID: "previous", seq: 8}
			_ = model.submit("下一轮")
			defer model.active.cancel()
			finished := status == "completed" || status == "cancelled"
			if (model.task.state == nil) != finished {
				t.Fatalf("status %s: task = %+v", status, model.task.state)
			}
			if finished && len(model.auxiliaryLines()) != 0 {
				t.Fatalf("finished task still visible: %v", model.auxiliaryLines())
			}
			if model.task.turnID != "" || model.task.seq != 0 {
				t.Fatal("previous turn event identity retained")
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
