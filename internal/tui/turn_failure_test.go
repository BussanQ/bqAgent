package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTurnFailureBoundsExternalAgentDiagnostics(t *testing.T) {
	for _, message := range []string{
		strings.Repeat("stderr: command failed\r\n", 40) + "hidden-tail",
		strings.Repeat("调用失败🙂", 200) + "hidden-tail",
	} {
		model := toolDrawerModel(t, 0)
		model.width = 24
		model.sequence = 1
		model.active = &activeTurn{sequence: 1, events: make(chan turnEvent), cancel: func() {}}
		model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", err: errors.New(message)})
		printed := strings.Join(model.printQueue, "\n")
		lines := strings.Split(printed, "\n")
		if len(lines) != 4 || !strings.Contains(printed, "本轮失败") || !strings.Contains(printed, "…") || strings.Contains(printed, "hidden-tail") {
			t.Fatalf("turn failure must retain its title and at most three diagnostic rows: %q", printed)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > model.contentWidth() {
				t.Fatalf("failure preview implicitly wraps: %q", line)
			}
		}
		if model.active != nil || model.phase != phaseIdle {
			t.Fatal("error display changed turn completion handling")
		}
	}
}

func TestRecoverableToolFailureDoesNotMarkTurnFailed(t *testing.T) {
	model := toolDrawerModel(t, 0)
	model.sequence = 1
	model.active = &activeTurn{sequence: 1, events: make(chan turnEvent), cancel: func() {}}
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "tool", tool: ToolEvent{
		Kind: "tool_result", ID: "failed-call", Name: "execute_bash", Status: "failed",
		Preview: strings.Repeat("recoverable error\n", 40) + "hidden-tail",
	}})
	model.handleTurnEvent(turnEvent{sequence: 1, kind: "done", result: TurnResult{Reply: "已修复并完成"}})
	view := model.View().Content + strings.Join(model.printQueue, "\n")
	if !strings.Contains(view, "已修复并完成") || strings.Contains(view, "本轮失败") || strings.Contains(view, "hidden-tail") {
		t.Fatalf("recoverable error obscured successful completion: %q", view)
	}
}
