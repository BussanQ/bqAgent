package server

import (
	"encoding/json"
	"testing"

	"bqagent/internal/extagent"
)

func TestClaudeApprovalHistoryKeepsReviewDetailAndFinalDecision(t *testing.T) {
	timeline := &externalTimeline{name: extagent.AgentClaude}
	req := extagent.ACPPermissionRequest{
		RequestID: "card", Agent: extagent.AgentClaude,
		ToolCall: json.RawMessage(`{"source":"claude-cli","tool":"ExitPlanMode","title":"审阅计划","detail":"计划内容"}`),
		Options:  []extagent.ACPPermissionOption{{OptionID: "allow", Name: "批准计划"}, {OptionID: "deny", Name: "继续规划"}},
	}
	timeline.recordPrompt(req)
	req.Selected = "deny"
	timeline.recordPrompt(req)
	rows := timeline.parts.Snapshot()
	if len(rows) != 1 || rows[0].Prompt == nil {
		t.Fatal(rows)
	}
	p := rows[0].Prompt
	if p.Title != "审阅计划" || p.Header != "ExitPlanMode" || p.Detail != "计划内容" || p.State != "selected" || p.Choice != "继续规划" || len(p.Options) != 2 {
		t.Fatal(p)
	}
}
