package serverchan

import "testing"

func TestBotStateStoreLoadMissingReturnsDefaultState(t *testing.T) {
	store := NewBotStateStore(t.TempDir())
	state, err := store.Load(42)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state.ChatID != 42 {
		t.Fatalf("ChatID = %d, want 42", state.ChatID)
	}
	if state.SessionID != "" {
		t.Fatalf("SessionID = %q, want empty", state.SessionID)
	}
}

func TestBotStateStorePersistsState(t *testing.T) {
	store := NewBotStateStore(t.TempDir())
	want := BotChatState{
		ChatID:                42,
		SessionID:             "session-1",
		LastCompletedUpdateID: 7,
		PendingUpdateID:       8,
		PendingReply:          "reply",
		LastError:             "failed",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := store.Load(42)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.ChatID != want.ChatID {
		t.Fatalf("ChatID = %d, want %d", got.ChatID, want.ChatID)
	}
	if got.SessionID != want.SessionID {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.LastCompletedUpdateID != want.LastCompletedUpdateID {
		t.Fatalf("LastCompletedUpdateID = %d, want %d", got.LastCompletedUpdateID, want.LastCompletedUpdateID)
	}
	if got.PendingUpdateID != want.PendingUpdateID {
		t.Fatalf("PendingUpdateID = %d, want %d", got.PendingUpdateID, want.PendingUpdateID)
	}
	if got.PendingReply != want.PendingReply {
		t.Fatalf("PendingReply = %q, want %q", got.PendingReply, want.PendingReply)
	}
	if got.LastError != want.LastError {
		t.Fatalf("LastError = %q, want %q", got.LastError, want.LastError)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want persisted timestamp")
	}
}
