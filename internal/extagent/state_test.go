package extagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStoreSavesToSharedPath(t *testing.T) {
	root := t.TempDir()
	store := NewStateStore(root)

	if err := store.Save(SessionState{
		BQSessionID:       "session-1",
		Agent:             AgentClaude,
		ExternalSessionID: "claude-session-1",
	}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".agent", "external-agents", "sessions", "session-1.json")); err != nil {
		t.Fatalf("shared state path stat error = %v, want saved file", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent", "server", "external-agents", "sessions", "session-1.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy state path stat error = %v, want not exist", err)
	}
}

func TestStateStoreLoadsLegacyServerPath(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, ".agent", "server", "external-agents", "sessions")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	content := []byte("{\n  \"bq_session_id\": \"session-legacy\",\n  \"agent\": \"codex\",\n  \"external_session_id\": \"external-1\"\n}\n")
	if err := os.WriteFile(filepath.Join(legacyDir, "session-legacy.json"), content, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	store := NewStateStore(root)
	state, err := store.Load("session-legacy")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if state.Agent != AgentCodex {
		t.Fatalf("agent = %q, want %q", state.Agent, AgentCodex)
	}
	if state.ExternalSessionID != "external-1" {
		t.Fatalf("external session id = %q, want %q", state.ExternalSessionID, "external-1")
	}
}
