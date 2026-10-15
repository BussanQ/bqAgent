package tui

import (
	"context"
	"strings"
	"testing"

	"bqagent/internal/turn"
	"github.com/charmbracelet/x/ansi"
)

func TestContextStatusReplacesSnapshotsAndSurvivesTurns(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.submit("first")
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "usage", usage: TokenUsage{InputTokens: 900000, OutputTokens: 10000}})
	for _, usage := range []turn.ContextUsage{
		{Tokens: 90000, Limit: 200000},
		{Tokens: 18000, Limit: 200000, Estimated: true},
	} {
		model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "context", contextUsage: usage})
		want := "45%/200K"
		if usage.Estimated {
			want = "9%/200K"
		}
		for _, width := range []int{40, 180} {
			status := ansi.Strip(model.renderStatus(width))
			if !strings.HasPrefix(status, want) || strings.Contains(status, "450%") {
				t.Fatalf("width=%d status=%q, want %q", width, status, want)
			}
			for _, line := range strings.Split(status, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("status exceeds terminal width: %q", line)
				}
			}
		}
	}
	model.handleTurnEvent(turnEvent{sequence: model.sequence - 1, kind: "context", contextUsage: turn.ContextUsage{Tokens: 199000, Limit: 200000}})
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", result: TurnResult{Reply: "done"}})
	model.submit("next")
	if model.contextUsage == nil || model.contextUsage.Tokens != 18000 || !strings.Contains(model.renderStatus(180), "9%/200K") {
		t.Fatal("context snapshot lost at completion or between turns")
	}
	model.clearSession()
	if model.contextUsage != nil || strings.Contains(model.renderStatus(180), "9%/200K") {
		t.Fatal("clear retained previous session context")
	}
}

type contextEventBackend struct{ fakeBackend }

func (*contextEventBackend) RunTurn(_ context.Context, _, _ string, events TurnEvents) (TurnResult, error) {
	events.Context(turn.ContextUsage{Tokens: 18000, Limit: 200000, Estimated: true})
	return TurnResult{}, nil
}

func TestLaunchTurnForwardsContextUsage(t *testing.T) {
	events := make(chan turnEvent, 2)
	message := launchTurn(&contextEventBackend{}, context.Background(), 7, "", "hello", TurnAttachments{}, events)().(turnEvent)
	if message.sequence != 7 || message.kind != "context" || message.contextUsage.Tokens != 18000 || message.contextUsage.Limit != 200000 {
		t.Fatalf("context event=%+v", message)
	}
	if done := <-events; done.kind != "done" {
		t.Fatalf("last event=%+v", done)
	}
}
