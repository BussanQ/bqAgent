package session

import (
	"errors"
	"testing"
)

func TestSessionStorePersistsMessagesAndStatus(t *testing.T) {
	store := NewStore(t.TempDir())
	savedSession, err := store.Create("inspect repo", true, true)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if savedSession.Meta().Status != StatusCreated {
		t.Fatalf("initial status = %q, want %q", savedSession.Meta().Status, StatusCreated)
	}

	messages := []map[string]any{
		{"role": "system", "content": "prompt"},
		{"role": "user", "content": "hello"},
	}
	if err := savedSession.RecordMessages(messages...); err != nil {
		t.Fatalf("RecordMessages returned error: %v", err)
	}
	if err := savedSession.MarkRunning(); err != nil {
		t.Fatalf("MarkRunning returned error: %v", err)
	}
	if err := savedSession.MarkFailed(errors.New("boom")); err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}

	reopened, err := store.Open(savedSession.ID())
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	loadedMessages, err := reopened.LoadMessages()
	if err != nil {
		t.Fatalf("LoadMessages returned error: %v", err)
	}
	if len(loadedMessages) != len(messages) {
		t.Fatalf("loaded messages = %d, want %d", len(loadedMessages), len(messages))
	}
	if reopened.Meta().Status != StatusFailed {
		t.Fatalf("reopened status = %q, want %q", reopened.Meta().Status, StatusFailed)
	}
	if reopened.Meta().LastError != "boom" {
		t.Fatalf("last error = %q, want %q", reopened.Meta().LastError, "boom")
	}
}
