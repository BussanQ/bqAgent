package qq

import (
	"path/filepath"
	"testing"
)

func TestGatewayStateStoreLoadMissingReturnsEmptyState(t *testing.T) {
	store := NewGatewayStateStore(t.TempDir())
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state.SessionID != "" || state.Seq != 0 {
		t.Fatalf("state = %+v, want empty", state)
	}
}

func TestGatewayStateStorePersistsState(t *testing.T) {
	agentDir := t.TempDir()
	store := NewGatewayStateStore(agentDir)
	if store.path != filepath.Join(agentDir, "server", "qq-bot", "gateway.json") {
		t.Fatalf("path = %q, want global agent server path", store.path)
	}
	want := GatewaySessionState{SessionID: "session-1", Seq: 42, LastError: "failed"}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.SessionID != want.SessionID || got.Seq != want.Seq || got.LastError != want.LastError {
		t.Fatalf("state = %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt is zero")
	}
}

func TestGatewayStateStoreClearSession(t *testing.T) {
	store := NewGatewayStateStore(t.TempDir())
	if err := store.Save(GatewaySessionState{SessionID: "session-1", Seq: 42, LastError: "failed"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.ClearSession(); err != nil {
		t.Fatalf("ClearSession() error = %v", err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if state.SessionID != "" || state.Seq != 0 || state.LastError != "failed" {
		t.Fatalf("state = %+v", state)
	}
}
