package tui

import (
	"context"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestActivityReportsStateAndSessionChanges(t *testing.T) {
	var reports [][2]string
	model := NewModel(&fakeBackend{}, Config{Workspace: t.TempDir(), AgentDir: t.TempDir(), ActivitySink: func(state, id string) { reports = append(reports, [2]string{state, id}) }})
	updated, _ := model.Update(startupMsg{})
	model = updated.(Model)
	model.submit("hello")
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "session", text: "new-session"})
	before := len(reports)
	model.handleTurnEvent(turnEvent{sequence: model.sequence - 1, kind: "session", text: "stale"})
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "token", text: "many tokens"})
	if len(reports) != before {
		t.Fatal("stale session or token caused report")
	}
	model.handleTurnEvent(turnEvent{sequence: model.sequence, kind: "done", err: context.Canceled})
	model.submit("/provider")
	model.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	model.clearSession()
	want := [][2]string{{"idle", ""}, {"working", ""}, {"working", "new-session"}, {"idle", "new-session"}, {"blocked", "new-session"}, {"idle", "new-session"}, {"idle", ""}}
	if !reflect.DeepEqual(reports, want) {
		t.Fatalf("reports=%v, want %v", reports, want)
	}
}

type sessionEventBackend struct{ fakeBackend }

func (*sessionEventBackend) RunTurn(_ context.Context, _, _ string, events TurnEvents) (TurnResult, error) {
	events.Session("session-before-output")
	events.Token("answer")
	return TurnResult{SessionID: "session-before-output"}, nil
}

func TestLaunchTurnForwardsSessionBeforeTokens(t *testing.T) {
	events := make(chan turnEvent, 3)
	first := launchTurn(&sessionEventBackend{}, context.Background(), 8, "", "hello", events)().(turnEvent)
	if first.kind != "session" || first.text != "session-before-output" || first.sequence != 8 {
		t.Fatalf("first=%+v", first)
	}
	if second := <-events; second.kind != "token" {
		t.Fatalf("second=%+v", second)
	}
	if last := <-events; last.kind != "done" {
		t.Fatalf("last=%+v", last)
	}
}
