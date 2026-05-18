package qq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateStoreLoadMissingReturnsDefaultState(t *testing.T) {
	store := NewStateStore(t.TempDir())
	state, err := store.Load("qq:c2c:user-1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state.PeerKey != "qq:c2c:user-1" {
		t.Fatalf("PeerKey = %q", state.PeerKey)
	}
	if state.SessionID != "" {
		t.Fatalf("SessionID = %q", state.SessionID)
	}
}

func TestStateStorePersistsState(t *testing.T) {
	store := NewStateStore(t.TempDir())
	want := ChatState{
		PeerKey:          "qq:group:group-1:member-1",
		SessionID:        "session-1",
		LastCompletedKey: "event-1",
		PendingKey:       "event-2",
		PendingReply:     "reply",
		LastError:        "failed",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(want.PeerKey)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.SessionID != want.SessionID || got.LastCompletedKey != want.LastCompletedKey || got.PendingKey != want.PendingKey || got.PendingReply != want.PendingReply || got.LastError != want.LastError {
		t.Fatalf("state = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt is zero")
	}
}

func TestStateStoreUsesSafeFilenames(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)
	peerKey := "qq:group:group/with/slash:member-1"
	if err := store.Save(ChatState{PeerKey: peerKey}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path := store.statePath(peerKey)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	name := filepath.Base(path)
	if strings.Contains(name, ":") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		t.Fatalf("unsafe filename = %q", name)
	}
}
