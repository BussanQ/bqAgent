package weixin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPollerStateStoreLoadEmptyReturnsInitialState(t *testing.T) {
	agentDir := t.TempDir()
	store := NewPollerStateStore(agentDir)
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(store.path, []byte(" \n\t"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state != (PollerState{}) {
		t.Fatalf("state = %#v, want initial state", state)
	}
}

func TestPollerStateStorePersistsState(t *testing.T) {
	agentDir := t.TempDir()
	store := NewPollerStateStore(agentDir)
	if store.path != filepath.Join(agentDir, "server", "weixin", "poller.json") {
		t.Fatalf("path = %q, want global agent server path", store.path)
	}
	want := PollerState{GetUpdatesBuf: "cursor-1", LastError: "temporary failure"}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got.GetUpdatesBuf != want.GetUpdatesBuf {
		t.Fatalf("GetUpdatesBuf = %q, want %q", got.GetUpdatesBuf, want.GetUpdatesBuf)
	}
	if got.LastError != want.LastError {
		t.Fatalf("LastError = %q, want %q", got.LastError, want.LastError)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want persisted timestamp")
	}
}
