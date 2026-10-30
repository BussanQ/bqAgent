package tui

import (
	"strings"
	"testing"
)

func TestToolGroupBackgroundExecutionStatus(t *testing.T) {
	for _, terminal := range []string{"succeeded", "failed", "killed"} {
		t.Run(terminal, func(t *testing.T) {
			group := toolGroup{}
			group.add(ToolEvent{Kind: "tool_start", ID: "start", Name: "bash", Status: "running"})
			group.add(ToolEvent{Kind: "tool_result", ID: "start", Name: "bash", Status: "running", CallStatus: "success", ExecutionStatus: "running", JobID: "job"})
			r, s, f := group.counts()
			if r != 1 || s != 0 || f != 0 {
				t.Fatalf("running counts: %d %d %d", r, s, f)
			}
			text := group.render(newTheme(true), false, 120)
			if strings.Contains(text, "✓") || !strings.Contains(text, "后台运行中") {
				t.Fatal(text)
			}
			execution := terminal
			if terminal == "succeeded" {
				execution = "completed"
			}
			code := 0
			delivered := true
			group.add(ToolEvent{Kind: "tool_result", ID: "wait", Name: "bash_wait", Status: terminal, CallStatus: "success", ExecutionStatus: execution, JobID: "job", ExitCode: &code, ResultDelivered: &delivered})
			r, s, f = group.counts()
			if r != 0 || (terminal == "succeeded" && s != 2) || (terminal != "succeeded" && f != 2) {
				t.Fatalf("final counts: %d %d %d", r, s, f)
			}
			if group.items[0].result.ExecutionStatus != execution {
				t.Fatal("original running card not updated")
			}
			detail := strings.Join(group.items[0].detailLines(120), "\n")
			if !strings.Contains(detail, "后台任务 ID：job") || !strings.Contains(detail, "最终结果已交付：true") {
				t.Fatal(detail)
			}
			restored := historyToolGroup([]ToolEvent{group.items[0].result, group.items[1].result})
			if restored.items[0].result.JobID != "job" || restored.items[0].result.ResultDelivered == nil {
				t.Fatal("history restore dropped execution metadata")
			}
			if terminal == "killed" && !strings.Contains(detail, "已终止") {
				t.Fatal(detail)
			}
		})
	}
}
