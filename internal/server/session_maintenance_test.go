package server

import (
	"testing"

	"bqagent/internal/session"
)

func TestNewServiceMaintainsExistingCompactSessions(t *testing.T) {
	root := t.TempDir()
	fullStore := session.NewStore(root, session.Options{TranscriptMode: session.TranscriptModeFull, OutputMaxBytes: session.DefaultOutputMaxBytes})
	savedSession, err := fullStore.Create(session.CreateOptions{Task: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := savedSession.RecordMessage(map[string]any{"role": "tool", "content": "legacy raw result"}); err != nil {
		t.Fatal(err)
	}
	if err := savedSession.SaveWorkingMessages([]map[string]any{{"role": "assistant", "content": "compact summary"}}); err != nil {
		t.Fatal(err)
	}
	if err := savedSession.MarkCompleted(); err != nil {
		t.Fatal(err)
	}

	options := session.Options{TranscriptMode: session.TranscriptModeCompact, OutputMaxBytes: session.DefaultOutputMaxBytes}
	_ = NewService(ServiceOptions{WorkspaceRoot: root, SessionOptions: &options})
	messages, err := savedSession.LoadMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0]["content"] != "compact summary" {
		t.Fatalf("maintained messages = %#v", messages)
	}
}
