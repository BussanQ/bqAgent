package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"bqagent/internal/taskstate"
	"bqagent/internal/turn"
)

// taskProgress is a projection of persisted task snapshots, not another plan.
type taskProgress struct {
	state  *taskstate.State
	turnID string
	seq    uint64
	// stale marks a snapshot the agent reported as predating the turn's work.
	stale bool
}

func (progress *taskProgress) apply(event turn.Event) bool {
	if event.Kind != "task.updated" || event.TurnID == "" || event.ItemID == "" ||
		(progress.turnID != "" && event.TurnID != progress.turnID) || event.Seq <= progress.seq {
		return false
	}
	var state *taskstate.State
	if err := json.Unmarshal([]byte(event.Text), &state); err != nil {
		return false
	}
	if state != nil && state.Validate() != nil {
		return false
	}
	progress.state, progress.turnID, progress.seq = state, event.TurnID, event.Seq
	progress.stale = event.Phase == "stale"
	return true
}

func (progress taskProgress) lines() []string {
	state := progress.state
	if state == nil {
		return nil
	}
	status := map[string]string{
		"active": "进行中", "completed": "已完成", "blocked": "受阻",
		"awaiting_input": "等待输入", "cancelled": "已取消",
	}[state.Status]
	if progress.stale {
		status += "（记录未更新）"
	}
	done := 0
	for _, criterion := range state.Criteria {
		if criterion.Done {
			done++
		}
	}
	lines := []string{fmt.Sprintf("任务 · %s · 验收 %d/%d · %s", status, done, len(state.Criteria), previewText(state.Goal))}
	for i, criterion := range state.Criteria {
		if i == 2 {
			lines = append(lines, fmt.Sprintf("  …另有 %d 项验收条件", len(state.Criteria)-i))
			break
		}
		mark := "[ ]"
		if criterion.Done {
			mark = "[✓]"
		}
		lines = append(lines, "  "+mark+" "+previewText(criterion.Description))
	}
	if len(state.Remaining) > 0 {
		lines = append(lines, fmt.Sprintf("剩余 %d 项 · %s", len(state.Remaining), previewText(strings.Join(state.Remaining, "；"))))
	}
	if state.Reason != "" {
		lines = append(lines, "原因 · "+previewText(state.Reason))
	}
	return lines
}
