package main

import (
	"testing"

	"bqagent/internal/agent"
	apptrace "bqagent/internal/trace"
	apptui "bqagent/internal/tui"
)

func TestToolEventSinkPreservesExecutionState(t *testing.T) {
	code := 7
	delivered := true
	var got apptui.ToolEvent
	sink := toolEventSink{emit: func(event apptui.ToolEvent) { got = event }}
	sink.EmitToolEvent(agent.ToolEvent{Kind: "tool_result", ID: "wait", Name: "bash_wait", Status: "failed", ToolExecution: apptrace.ToolExecution{CallStatus: "error", ExecutionStatus: "failed", JobID: "job", ExitCode: &code, ResultDelivered: &delivered}})
	if got.CallStatus != "error" || got.ExecutionStatus != "failed" || got.JobID != "job" || got.ExitCode == nil || *got.ExitCode != 7 || got.ResultDelivered == nil || !*got.ResultDelivered {
		t.Fatalf("execution metadata lost: %+v", got)
	}
}
