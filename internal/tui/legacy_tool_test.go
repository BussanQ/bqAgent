package tui

import (
	"strings"
	"testing"
)

func TestRetiredToolHistoryUsesGenericRendering(t *testing.T) {
	// Retired calls stay readable as history, but cannot create task progress.
	for _, name := range []string{"todo_write", "retired_checklist"} {
		start := ToolEvent{Kind: "tool_start", ID: "old-call", Seq: 1, Name: name, Arguments: map[string]any{"todos": `[{"content":"old item","status":"completed"}]`}}
		result := ToolEvent{Kind: "tool_result", ID: start.ID, Name: name, Status: "succeeded", Preview: "historical output"}
		for _, event := range []ToolEvent{start, result} {
			got := toolSummary(event, newTheme(true), 160, false)
			if strings.Contains(got, "\n") || strings.Contains(got, "任务进度") {
				t.Fatalf("retired tool created a task block: %q", got)
			}
		}
		result.Arguments = start.Arguments
		group := historyToolGroup([]ToolEvent{result})
		got := group.items[0].render(newTheme(true), 160, true)
		for _, want := range []string{name, "todos=", "old item", "historical output"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing generic history field %q in %q", want, got)
			}
		}
		model := NewModel(&fakeBackend{history: History{Messages: []HistoryMessage{{Role: "assistant", Tools: []ToolEvent{result}}}}}, Config{SessionID: "old", Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
		if model.task.state != nil || len(model.auxiliaryLines()) != 0 {
			t.Fatal("old tool history created current task progress")
		}
	}
}
