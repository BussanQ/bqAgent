package weixin

import "testing"

func TestChatStateStoreLoadMissingReturnsDefaultState(t *testing.T) {
	store := NewChatStateStore(t.TempDir())
	state, err := store.Load("user-1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state.UserID != "user-1" {
		t.Fatalf("UserID = %q, want %q", state.UserID, "user-1")
	}
	if state.SessionID != "" {
		t.Fatalf("SessionID = %q, want empty", state.SessionID)
	}
}

func TestChatStateStorePersistsState(t *testing.T) {
	store := NewChatStateStore(t.TempDir())
	want := ChatState{
		UserID:                    "user-1",
		SessionID:                 "session-1",
		LastCompletedContextToken: "ctx-1",
		PendingContextToken:       "ctx-2",
		PendingReply:              "reply",
		LastError:                 "failed",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := store.Load("user-1")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.UserID != want.UserID {
		t.Fatalf("UserID = %q, want %q", got.UserID, want.UserID)
	}
	if got.SessionID != want.SessionID {
		t.Fatalf("SessionID = %q, want %q", got.SessionID, want.SessionID)
	}
	if got.LastCompletedContextToken != want.LastCompletedContextToken {
		t.Fatalf("LastCompletedContextToken = %q, want %q", got.LastCompletedContextToken, want.LastCompletedContextToken)
	}
	if got.PendingContextToken != want.PendingContextToken {
		t.Fatalf("PendingContextToken = %q, want %q", got.PendingContextToken, want.PendingContextToken)
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
