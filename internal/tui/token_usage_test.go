package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTokenUsageStatusSurvivesToolsAndCompletion(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.submit("search")
	if got := model.tokenUsageStatus(); got != "in —· out —" {
		t.Fatalf("before first request = %q", got)
	}
	usage := TokenUsage{InputTokens: 12345, OutputTokens: 12, OutputEstimated: true, Calls: 2}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "usage", usage: usage})
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "token", text: "a"})
	if got := model.renderStatus(100); !strings.Contains(got, "in 12.3K· out 12") {
		t.Fatalf("active status = %q", got)
	}
	usage.OutputTokens, usage.OutputEstimated = 58, false
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "usage", usage: usage})
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "tool", tool: ToolEvent{Kind: "tool_start", ID: "call-1", Name: "lookup"}})
	if got := model.renderStatus(40); !strings.Contains(got, "in 12.3K· out 58") {
		t.Fatalf("tool phase in narrow terminal = %q", got)
	}
	model.handleTurnEvent(turnEvent{sequence: model.sequence - 1, kind: "usage", usage: TokenUsage{OutputTokens: 999}})
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", result: TurnResult{Metrics: &GenerationMetrics{PromptTokens: 100, CompletionTokens: 8}}})
	if got := model.tokenUsageStatus(); got != "in 12.3K· out 58" {
		t.Fatalf("completion must retain whole-turn usage, not final-call metrics: %q", got)
	}
	model.submit("next")
	if model.usage != nil || model.metrics != nil {
		t.Fatal("new turn retained previous usage")
	}
	model.usage = &usage
	model.clearSession()
	if model.usage != nil || model.tokenUsageStatus() != "in —· out —" {
		t.Fatal("clear retained previous usage")
	}
}

func TestFormatTokenCountCompactUnits(t *testing.T) {
	for _, test := range []struct {
		count int
		want  string
	}{
		{-1, "0"}, {0, "0"}, {1, "1"}, {999, "999"},
		{1000, "1K"}, {12345, "12.3K"}, {982338, "982.3K"},
		{999949, "999.9K"}, {999950, "1M"}, {999999, "1M"},
		{1000000, "1M"}, {1234567, "1.2M"}, {12500000, "12.5M"},
	} {
		if got := formatTokenCount(test.count); got != test.want {
			t.Fatalf("count %d = %q, want %q", test.count, got, test.want)
		}
	}
}

func TestTokenUsageStatusCompactUnitsForAllSources(t *testing.T) {
	for _, test := range []struct {
		name  string
		model Model
		want  string
	}{
		{"usage", Model{usage: &TokenUsage{InputTokens: 1234567, OutputTokens: 8707}}, "in 1.2M· out 8.7K"},
		{"estimated usage", Model{usage: &TokenUsage{InputTokens: 1234567, OutputTokens: 8707, InputEstimated: true, OutputEstimated: true}}, "in 1.2M· out 8.7K"},
		{"metrics", Model{metrics: &GenerationMetrics{PromptTokens: 982338, CompletionTokens: 8707}}, "in 982.3K· out 8.7K"},
		{"live", Model{liveRunes: 4000, liveTokens: 1000}, "in —· out 1K"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.model.tokenUsageStatus(); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

type usageEventBackend struct{ fakeBackend }

func (*usageEventBackend) RunTurn(_ context.Context, _, _ string, events TurnEvents) (TurnResult, error) {
	events.Usage(TokenUsage{InputTokens: 100, OutputTokens: 12, Calls: 1})
	return TurnResult{}, nil
}

func TestLaunchTurnForwardsLiveUsage(t *testing.T) {
	events := make(chan turnEvent, 2)
	cmd := launchTurn(&usageEventBackend{}, context.Background(), 7, "", "hello", TurnAttachments{}, events)
	first := cmd().(turnEvent)
	if first.sequence != 7 || first.kind != "usage" || first.usage != (TokenUsage{InputTokens: 100, OutputTokens: 12, Calls: 1}) {
		t.Fatalf("first event = %+v", first)
	}
	if done := <-events; done.kind != "done" {
		t.Fatalf("last event = %+v", done)
	}
}

func TestSpeedLabelUsesLiveActiveTime(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		name  string
		model Model
		want  string
	}{
		{"no usage", Model{}, "TPS —"},
		{"no output yet", Model{usage: &TokenUsage{InputTokens: 100, ActiveSince: now.Add(-time.Second)}}, "TPS —"},
		{"streaming", Model{usage: &TokenUsage{OutputTokens: 50, ActiveSince: now.Add(-2 * time.Second)}}, "25.0 tok/s"},
		{"between requests", Model{usage: &TokenUsage{OutputTokens: 90, GenerationDuration: 3 * time.Second}}, "30.0 tok/s"},
		{"finished plus running", Model{usage: &TokenUsage{OutputTokens: 120, GenerationDuration: 3 * time.Second, ActiveSince: now.Add(-time.Second)}}, "30.0 tok/s"},
		{"metrics fallback", Model{usage: &TokenUsage{OutputTokens: 17}, metrics: &GenerationMetrics{TokensPerSecond: 42}}, "42.0 tok/s"},
	} {
		if got := test.model.speedLabel(now); got != test.want {
			t.Errorf("%s: speedLabel = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestRunningStatusShowsLiveSpeed(t *testing.T) {
	model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
	model.submit("search")
	defer model.active.cancel()
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "usage", usage: TokenUsage{OutputTokens: 40, GenerationDuration: 2 * time.Second}})
	if got := model.renderStatus(200); !strings.Contains(got, "20.0 tok/s") {
		t.Fatalf("running status = %q", got)
	}
}

func TestSpeedFreezesAfterCancel(t *testing.T) {
	for _, viaCancel := range []bool{true, false} {
		model := NewModel(&fakeBackend{}, Config{Context: context.Background(), Workspace: t.TempDir(), AgentDir: t.TempDir(), NoColor: true})
		model.submit("search")
		model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "usage", usage: TokenUsage{OutputTokens: 50, ActiveSince: time.Now().Add(-2 * time.Second)}})
		if viaCancel {
			model.cancelActiveTurn()
		} else {
			model.active.cancelled = true
			model.active.cancel()
		}
		// The collector's closing snapshot is dropped after cancellation.
		model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "usage", usage: TokenUsage{OutputTokens: 50, GenerationDuration: 2 * time.Second}})
		model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", err: context.Canceled})
		if model.usage == nil || !model.usage.ActiveSince.IsZero() {
			t.Fatalf("viaCancel=%v: in-flight interval not frozen: %+v", viaCancel, model.usage)
		}
		first := model.speedLabel(time.Now())
		if later := model.speedLabel(time.Now().Add(time.Hour)); later != first || first == "TPS —" {
			t.Fatalf("viaCancel=%v: speed kept changing after cancel: %q -> %q", viaCancel, first, later)
		}
	}
}
